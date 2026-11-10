package pluginview

// Data-oriented native widgets: the data grid, the graph, the plan graph, and
// the canvas. They are rendered host-side from the same retained tree as the
// core widget set; the guest owns the data and receives events, never drawing
// commands or host pointers.
//
// A data grid may declare a row count far above the rows in one patch. The
// host renders the visible rows through MyGo's virtualizing Table and asks the
// guest for a missing window with item_request, whose Index is the first
// missing display row and Width is how many rows are missing. The guest
// answers with a new patch of row nodes keyed by PropRowIndex, so a large data
// set never crosses the ABI in one batch.

import (
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
)

const (
	defaultGraphNodeWidth  = 120
	defaultGraphNodeHeight = 36
	graphColumnGap         = 60
	graphRowGap            = 36
	graphInset             = 12
)

// windowRequest remembers the last visible-window item request of one
// virtualizing surface, so a render only asks the guest when the window
// changes.
type windowRequest struct {
	requested bool
	from      int
	size      int
}

// differs reports whether from and size are a different window than the one
// last requested, and records them as the new window.
func (w *windowRequest) differs(from, size int) bool {
	if w.requested && w.from == from && w.size == size {
		return false
	}
	w.requested, w.from, w.size = true, from, size
	return true
}

// gridState is the host-owned place and sort state of one data grid. The
// selected row uses the shared ListState sync; the sort order is kept here so
// a table header click can be reported to the guest.
type gridState struct {
	list *ui.ListState

	sort    ui.SortOrder
	sortSet bool
	// pluginSort is the last sort fingerprint the guest declared, so a host
	// header click is not mistaken for a plugin patch and vice versa.
	pluginSort string

	request windowRequest
}

func (s *Surface) gridState(node *domainnativeui.Node) *gridState {
	state := s.grids[node.ID]
	if state == nil {
		selected := -1
		state = &gridState{list: &ui.ListState{Selected: &selected}}
		s.grids[node.ID] = state
	}
	return state
}

// dataGrid renders the visible window of a possibly huge row set.
func (s *Surface) dataGrid(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	state := s.gridState(node)

	var columns []ui.TableColumn
	var columnIDs []string
	rowsByIndex := make(map[int]domainnativeui.NodeID)
	rowCount := int(node.IntProp(domainnativeui.PropRowCount, 0))
	highest := -1
	for _, childID := range node.Children {
		child, ok := tree.Nodes[childID]
		if !ok || child == nil || child.BoolProp(domainnativeui.PropHidden, false) {
			continue
		}
		switch child.Kind {
		case domainnativeui.NodeDataColumn:
			title := child.StringProp(domainnativeui.PropLabel)
			if title == "" {
				title = child.Text
			}
			id := title
			if id == "" {
				id = "column-" + strconv.Itoa(len(columns))
			}
			columns = append(columns, ui.TableColumn{
				Title:    title,
				ID:       id,
				Width:    float32(child.FloatProp(domainnativeui.PropWidth, 0)),
				Sortable: child.BoolProp(domainnativeui.PropSortable, false),
			})
			columnIDs = append(columnIDs, id)
		case domainnativeui.NodeDataRow:
			index := int(child.IntProp(domainnativeui.PropRowIndex, -1))
			if index >= 0 {
				rowsByIndex[index] = childID
				if index > highest {
					highest = index
				}
			}
		}
	}
	if len(columns) == 0 {
		columns = []ui.TableColumn{{Title: "", ID: "column-0"}}
		columnIDs = []string{"column-0"}
	}
	if rowCount <= highest {
		rowCount = highest + 1
	}
	if rowCount < 0 {
		rowCount = 0
	}

	s.syncListChoice(node, state.list)
	previousSelected := -1
	if state.list.Selected != nil {
		previousSelected = *state.list.Selected
	}

	// A sortable grid keeps a live SortOrder so MyGo reports header clicks.
	sortable := false
	for _, column := range columns {
		if column.Sortable {
			sortable = true
			break
		}
	}
	if sortable {
		column := int(node.IntProp(domainnativeui.PropSortColumn, -1))
		descending := node.BoolProp(domainnativeui.PropSortDescending, false)
		fingerprint := strconv.Itoa(column) + ":" + strconv.FormatBool(descending)
		if !state.sortSet || state.pluginSort != fingerprint {
			state.sortSet = true
			state.pluginSort = fingerprint
			state.sort = ui.SortOrder{Descending: descending}
			if column >= 0 && column < len(columnIDs) {
				state.sort.Column = columnIDs[column]
			}
		}
		state.list.Sort = &state.sort
	}

	before := state.sort
	firstMissing, missingCount := -1, 0
	element := ui.Table(c, state.list, columns, rowCount, func(row, column int) {
		rowID, ok := rowsByIndex[row]
		if !ok {
			if column == 0 {
				if firstMissing < 0 {
					firstMissing = row
				}
				missingCount++
			}
			return
		}
		rowNode, ok := tree.Nodes[rowID]
		if !ok || rowNode == nil || column >= len(rowNode.Children) {
			return
		}
		s.dataGridCell(c, tree, rowNode.Children[column])
	})
	if width := float32(node.FloatProp(domainnativeui.PropWidth, 0)); width > 0 {
		element = element.Width(width)
	}
	if height := float32(node.FloatProp(domainnativeui.PropHeight, 0)); height > 0 {
		element = element.Height(height)
	}
	if node.BoolProp(domainnativeui.PropDisabled, false) {
		element = element.Disabled(true)
	}
	// A header click changes the sort order and makes MyGo report Changed,
	// but it does not select a row. Emit selection only when the selected
	// row actually changed, so sorting never fabricates a row selection.
	if element.Changed() && state.list.Selected != nil && *state.list.Selected != previousSelected {
		s.emit(node.ID, domainnativeui.Event{
			Kind: domainnativeui.EventSelection, Index: int32(*state.list.Selected),
		})
	}
	if element.Submitted() && state.list.Selected != nil {
		s.emit(node.ID, domainnativeui.Event{
			Kind: domainnativeui.EventSubmit, Index: int32(*state.list.Selected),
		})
	}
	if sortable && (state.sort != before) {
		index := -1
		for i, id := range columnIDs {
			if id == state.sort.Column {
				index = i
				break
			}
		}
		direction := "asc"
		if state.sort.Descending {
			direction = "desc"
		}
		s.emit(node.ID, domainnativeui.Event{
			Kind: domainnativeui.EventChange, Index: int32(index), Text: direction,
		})
	}

	// Ask for the first missing visible window. The request is coalesced by
	// the event queue and only re-sent when the window changes, so scrolling a
	// huge grid cannot flood the guest.
	if firstMissing >= 0 {
		if state.request.differs(firstMissing, missingCount) {
			s.emit(node.ID, domainnativeui.Event{
				Kind:  domainnativeui.EventItemRequest,
				Index: int32(firstMissing),
				Width: float32(missingCount),
			})
		}
	} else {
		state.request.requested = false
	}

	// renderNode wraps the table with the shared input binding, which reports
	// the wheel on this data_grid node; the table scrolls its virtual window
	// itself.
	return element
}

// dataGridCell draws one cell without input bindings. MyGo routes a wheel to
// the innermost element that has a handler, so cells stay inert and the
// grid's own scroll handler receives the gesture and attributes it to the
// data_grid node.
func (s *Surface) dataGridCell(c *ui.Context, tree *domainnativeui.Tree,
	id domainnativeui.NodeID) ui.Element {
	cell, ok := tree.Nodes[id]
	if !ok || cell == nil || cell.BoolProp(domainnativeui.PropHidden, false) {
		return ui.Element{}
	}
	nc := c.Key(nodeKey{id: id})
	switch cell.Kind {
	case domainnativeui.NodeText:
		return ui.Text(nc, cell.Text)
	case domainnativeui.NodeBadge:
		return ui.Badge(nc, cell.Text)
	case domainnativeui.NodeProgress:
		return s.progress(nc, cell)
	case domainnativeui.NodeImage:
		return s.image(nc, tree, cell)
	default:
		return s.renderNode(c, tree, id)
	}
}

// graphBox is one laid-out graph node in graph-local DIPs.
type graphBox struct {
	id       domainnativeui.NodeID
	index    int
	x, y     float32
	w, h     float32
	label    string
	hasX     bool
	hasY     bool
	selected bool
	disabled bool
}

type graphEdge struct {
	from, to domainnativeui.NodeID
	directed bool
	width    float32
	stroke   domainnativeui.Token
}

// graph renders nodes and edges with a host-side layout. Explicit x/y props
// win; otherwise LayoutRow, LayoutColumn, and LayoutGrid/Box choose a
// deterministic automatic layout, so the guest may send either.
func (s *Surface) graph(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	var boxes []graphBox
	var edges []graphEdge
	index := 0
	for _, childID := range node.Children {
		child, ok := tree.Nodes[childID]
		if !ok || child == nil || child.BoolProp(domainnativeui.PropHidden, false) {
			continue
		}
		switch child.Kind {
		case domainnativeui.NodeGraphNode:
			label := child.StringProp(domainnativeui.PropLabel)
			if label == "" {
				label = child.Text
			}
			xValue, hasX := child.Prop(domainnativeui.PropX)
			yValue, hasY := child.Prop(domainnativeui.PropY)
			box := graphBox{
				id:       childID,
				index:    index,
				w:        float32(child.FloatProp(domainnativeui.PropWidth, defaultGraphNodeWidth)),
				h:        float32(child.FloatProp(domainnativeui.PropHeight, defaultGraphNodeHeight)),
				label:    label,
				hasX:     hasX && xValue.Kind == domainnativeui.PropFloat,
				hasY:     hasY && yValue.Kind == domainnativeui.PropFloat,
				selected: child.IntProp(domainnativeui.PropSelected, 0) != 0,
				disabled: child.BoolProp(domainnativeui.PropDisabled, false),
			}
			if box.hasX {
				box.x = float32(xValue.Float)
			}
			if box.hasY {
				box.y = float32(yValue.Float)
			}
			boxes = append(boxes, box)
			index++
		case domainnativeui.NodeGraphEdge:
			from, okFrom := refNodeID(child, domainnativeui.PropFrom)
			to, okTo := refNodeID(child, domainnativeui.PropTo)
			if !okFrom || !okTo {
				continue
			}
			stroke := domainnativeui.TokenBorder
			if value, ok := child.Prop(domainnativeui.PropStrokeToken); ok && value.Kind == domainnativeui.PropInt {
				stroke = domainnativeui.Token(value.Int)
			}
			edges = append(edges, graphEdge{
				from:     from,
				to:       to,
				directed: child.BoolProp(domainnativeui.PropDirected, false),
				width:    float32(child.FloatProp(domainnativeui.PropLineWidth, 1)),
				stroke:   stroke,
			})
		}
	}

	autoLayoutGraph(boxes, domainnativeui.Layout(node.IntProp(domainnativeui.PropLayout, int64(domainnativeui.LayoutGrid))))

	width := float32(node.FloatProp(domainnativeui.PropWidth, 0))
	height := float32(node.FloatProp(domainnativeui.PropHeight, 0))
	extentW, extentH := graphExtent(boxes)
	if width < extentW {
		width = extentW
	}
	if height < extentH {
		height = extentH
	}
	if width <= 0 {
		width = graphInset * 2
	}
	if height <= 0 {
		height = graphInset * 2
	}

	byID := make(map[domainnativeui.NodeID]int, len(boxes))
	for i, box := range boxes {
		byID[box.id] = i
	}
	element := ui.Box(c).Width(width).Height(height)
	element = element.Draw(func(p *ui.Painter, r ui.Rect) {
		for _, edge := range edges {
			fromIndex, okFrom := byID[edge.from]
			toIndex, okTo := byID[edge.to]
			if !okFrom || !okTo {
				continue
			}
			from := centered(boxes[fromIndex])
			to := centered(boxes[toIndex])
			p.Line(from.x, from.y, to.x, to.y, edge.width, tokenColor(c, edge.stroke))
			if edge.directed {
				drawArrow(p, from, to, edge.width, tokenColor(c, edge.stroke))
			}
		}
	})
	element = element.Children(func() {
		for _, box := range boxes {
			s.graphNode(c, node, box)
		}
	})
	return element
}

// graphNode draws one node as a labeled box and reports its interaction on the
// graph node, with the pressed node as Related.
func (s *Surface) graphNode(c *ui.Context, graph *domainnativeui.Node, box graphBox) {
	background := domainnativeui.TokenSurface
	text := domainnativeui.TokenText
	if box.selected {
		background = domainnativeui.TokenAccent
		text = domainnativeui.TokenAccentText
	}
	if box.disabled {
		text = domainnativeui.TokenTextMuted
	}
	element := ui.Text(c, box.label).Absolute().
		Left(box.x).Top(box.y).Width(box.w).Height(box.h).Padding(8).
		FontSize(13).TextColor(tokenColor(c, text))
	element = element.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, tokenColor(c, background), 6)
		p.Stroke(r, tokenColor(c, domainnativeui.TokenBorder), 6, 1)
	})
	if box.disabled {
		return
	}
	if element.Clicked() {
		s.emit(graph.ID, domainnativeui.Event{
			Kind: domainnativeui.EventSelection, Index: int32(box.index),
			Text: box.label, Related: domainnativeui.IDFromNode(box.id),
		})
	}
	if element.DoubleClicked() {
		s.emit(graph.ID, domainnativeui.Event{
			Kind: domainnativeui.EventDoubleClick, Index: int32(box.index),
			Text: box.label, Related: domainnativeui.IDFromNode(box.id),
		})
	}
	if element.RightClicked() {
		s.emit(graph.ID, domainnativeui.Event{
			Kind: domainnativeui.EventContextMenu, Index: int32(box.index),
			Text: box.label, Related: domainnativeui.IDFromNode(box.id),
		})
	}
}

type point struct{ x, y float32 }

func centered(box graphBox) point {
	return point{x: box.x + box.w/2, y: box.y + box.h/2}
}

func drawArrow(p *ui.Painter, from, to point, width float32, color ui.Color) {
	angle := math.Atan2(float64(to.y-from.y), float64(to.x-from.x))
	const size = 10.0
	for _, offset := range []float64{2.6, -2.6} {
		x := to.x - float32(size*math.Cos(angle+offset))
		y := to.y - float32(size*math.Sin(angle+offset))
		p.Line(to.x, to.y, x, y, width, color)
	}
}

// autoLayoutGraph fills the coordinates the guest did not give. Row and
// Column lay the nodes in one line; Grid and Box wrap them into a square.
func autoLayoutGraph(boxes []graphBox, layout domainnativeui.Layout) {
	if len(boxes) == 0 {
		return
	}
	maxW, maxH := float32(0), float32(0)
	for _, box := range boxes {
		if box.w > maxW {
			maxW = box.w
		}
		if box.h > maxH {
			maxH = box.h
		}
	}
	stepX := maxW + graphColumnGap
	stepY := maxH + graphRowGap
	columns := 1
	switch layout {
	case domainnativeui.LayoutRow:
		columns = len(boxes)
	case domainnativeui.LayoutColumn:
		stepX = 0
	default:
		columns = int(math.Ceil(math.Sqrt(float64(len(boxes)))))
		if columns < 1 {
			columns = 1
		}
	}
	for i := range boxes {
		box := &boxes[i]
		if !box.hasX {
			if stepX == 0 {
				box.x = graphInset
			} else {
				box.x = graphInset + float32(i%columns)*stepX
			}
		}
		if !box.hasY {
			if stepX == 0 { // LayoutColumn: one per row
				box.y = graphInset + float32(i)*stepY
			} else {
				box.y = graphInset + float32(i/columns)*stepY
			}
		}
	}
}

func graphExtent(boxes []graphBox) (float32, float32) {
	width, height := float32(0), float32(0)
	for _, box := range boxes {
		if right := box.x + box.w + graphInset; right > width {
			width = right
		}
		if bottom := box.y + box.h + graphInset; bottom > height {
			height = bottom
		}
	}
	return width, height
}

// refNodeID resolves a stored node-reference prop to a live guest id.
func refNodeID(node *domainnativeui.Node, prop domainnativeui.PropID) (domainnativeui.NodeID, bool) {
	value, ok := node.Prop(prop)
	if !ok || value.Kind != domainnativeui.PropRef {
		return 0, false
	}
	return value.Ref.NodeID()
}

// planGraph renders a plan tree as indented rows with status, cost, and
// annotations. Its interaction is contained: every event is queued for the
// plan_graph node, and a plan node's handle is never sent to the surrounding
// container.
func (s *Surface) planGraph(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	disabled := node.BoolProp(domainnativeui.PropDisabled, false)
	element := ui.Column(c).Gap(2)
	element = element.Children(func() {
		index := 0
		var walk func(id domainnativeui.NodeID, depth int, visible bool)
		walk = func(id domainnativeui.NodeID, depth int, visible bool) {
			plan, ok := tree.Nodes[id]
			if !ok || plan == nil || !visible || plan.BoolProp(domainnativeui.PropHidden, false) {
				return
			}
			label := plan.Text
			if label == "" {
				label = plan.StringProp(domainnativeui.PropLabel)
			}
			expandedPtr := s.syncedBool(plan, plan.BoolProp(domainnativeui.PropExpanded, false))
			wasExpanded := *expandedPtr
			line := strings.Repeat("  ", depth) + planStatusPrefix(plan.IntProp(domainnativeui.PropPlanStatus, 0)) + label
			if cost := plan.FloatProp(domainnativeui.PropCost, 0); cost > 0 {
				line += "  " + strconv.FormatFloat(cost, 'g', -1, 64)
			}
			if annotation := plan.StringProp(domainnativeui.PropAnnotation); annotation != "" {
				line += " — " + annotation
			}
			rowIndex := index
			index++
			row := ui.Row(c).Padding(2).Label(label).Children(func() {
				ui.Text(c, line)
			})
			if !disabled && row.Clicked() {
				s.emit(node.ID, domainnativeui.Event{
					Kind: domainnativeui.EventSelection, Index: int32(rowIndex),
					Text: label, Related: domainnativeui.IDFromNode(plan.ID),
				})
			}
			if !disabled && row.DoubleClicked() {
				*expandedPtr = !*expandedPtr
				s.emit(node.ID, domainnativeui.Event{
					Kind: domainnativeui.EventSubmit, Index: int32(rowIndex),
					Text: label, Related: domainnativeui.IDFromNode(plan.ID),
				})
			}
			if !disabled && row.RightClicked() {
				s.emit(node.ID, domainnativeui.Event{
					Kind: domainnativeui.EventContextMenu, Index: int32(rowIndex),
					Text: label, Related: domainnativeui.IDFromNode(plan.ID),
				})
			}
			children := append([]domainnativeui.NodeID(nil), plan.Children...)
			for _, child := range children {
				walk(child, depth+1, *expandedPtr || wasExpanded)
			}
		}
		for _, child := range node.Children {
			walk(child, 0, true)
		}
	})
	return element
}

// planStatusPrefix marks a plan node with locale-neutral punctuation rather
// than host-authored words, so the host adds no untranslated display text.
func planStatusPrefix(status int64) string {
	switch domainnativeui.PlanStatus(status) {
	case domainnativeui.PlanRunning:
		return "> "
	case domainnativeui.PlanDone:
		return "= "
	case domainnativeui.PlanFailed:
		return "! "
	case domainnativeui.PlanSkipped:
		return "- "
	}
	return ""
}

// canvas draws its primitives and hosted images and reports pointer input in
// canvas-local DIPs: the host subtracts the canvas's own origin, never a
// window coordinate or a guest-provided transform.
func (s *Surface) canvas(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	width := float32(node.FloatProp(domainnativeui.PropWidth, 0))
	height := float32(node.FloatProp(domainnativeui.PropHeight, 0))

	var textShapes []domainnativeui.NodeID
	for _, childID := range node.Children {
		child, ok := tree.Nodes[childID]
		if !ok || child == nil || child.BoolProp(domainnativeui.PropHidden, false) {
			continue
		}
		if child.Kind == domainnativeui.NodeCanvasShape &&
			domainnativeui.CanvasShape(child.IntProp(domainnativeui.PropShape, -1)) == domainnativeui.CanvasText {
			textShapes = append(textShapes, childID)
		}
	}

	element := ui.Box(c).Width(width).Height(height)
	element = element.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, tokenColor(c, domainnativeui.TokenSurface), 4)
		for _, childID := range node.Children {
			shape, ok := tree.Nodes[childID]
			if !ok || shape == nil || shape.BoolProp(domainnativeui.PropHidden, false) {
				continue
			}
			if shape.Kind != domainnativeui.NodeCanvasShape {
				continue
			}
			s.drawCanvasShape(c, p, shape, r)
		}
	})
	element = element.Children(func() {
		for _, childID := range textShapes {
			shape, ok := tree.Nodes[childID]
			if !ok || shape == nil {
				continue
			}
			text := shape.Text
			if text == "" {
				text = shape.StringProp(domainnativeui.PropLabel)
			}
			size := float32(shape.FloatProp(domainnativeui.PropFontSize, 13))
			ui.Text(c, text).Absolute().
				Left(float32(shape.FloatProp(domainnativeui.PropX, 0))).
				Top(float32(shape.FloatProp(domainnativeui.PropY, 0))).
				FontSize(size).
				TextColor(tokenColor(c, canvasStroke(shape, domainnativeui.TokenText)))
		}
	})

	// A click, double click, context menu, or drag over the canvas reports
	// the pointer relative to the canvas's top-left corner.
	if disabled := node.BoolProp(domainnativeui.PropDisabled, false); disabled {
		return element
	}
	if x, y, over := element.PointerPosition(); over {
		if element.Clicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventClick, X: x, Y: y})
		}
		if element.DoubleClicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventDoubleClick, X: x, Y: y})
		}
		if element.RightClicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventContextMenu, X: x, Y: y, Button: 1})
		}
		if dx, dy, dragged := element.Dragged(); dragged && (dx != 0 || dy != 0) {
			s.emit(node.ID, domainnativeui.Event{
				Kind: domainnativeui.EventDrag, X: x, Y: y, DX: dx, DY: dy,
			})
		}
	}
	return element
}

// drawCanvasShape paints one primitive in canvas-local DIPs. The painter
// draws in window coordinates, so the canvas box's origin is added here.
func (s *Surface) drawCanvasShape(c *ui.Context, p *ui.Painter, shape *domainnativeui.Node,
	origin ui.Rect) {
	x := origin.X + float32(shape.FloatProp(domainnativeui.PropX, 0))
	y := origin.Y + float32(shape.FloatProp(domainnativeui.PropY, 0))
	w := float32(shape.FloatProp(domainnativeui.PropWidth, 0))
	h := float32(shape.FloatProp(domainnativeui.PropHeight, 0))
	rect := ui.Rect{X: x, Y: y, W: w, H: h}
	fill := canvasFill(shape)
	stroke := canvasStroke(shape, domainnativeui.TokenBorder)
	lineWidth := float32(shape.FloatProp(domainnativeui.PropLineWidth, 1))
	if lineWidth <= 0 {
		lineWidth = 1
	}
	switch domainnativeui.CanvasShape(shape.IntProp(domainnativeui.PropShape, int64(domainnativeui.CanvasRect))) {
	case domainnativeui.CanvasRect:
		p.Fill(rect, tokenColor(c, fill), 0)
		if _, ok := shape.Prop(domainnativeui.PropStrokeToken); ok {
			p.Stroke(rect, tokenColor(c, stroke), 0, lineWidth)
		}
	case domainnativeui.CanvasEllipse:
		radius := min(w, h) / 2
		p.Fill(rect, tokenColor(c, fill), radius)
		if _, ok := shape.Prop(domainnativeui.PropStrokeToken); ok {
			p.Stroke(rect, tokenColor(c, stroke), radius, lineWidth)
		}
	case domainnativeui.CanvasLine:
		p.Line(x, y, x+w, y+h, lineWidth, tokenColor(c, stroke))
	case domainnativeui.CanvasImage:
		if s.options.Assets != nil {
			if source, ok := s.options.Assets(shape.StringProp(domainnativeui.PropSrc)); ok {
				p.Image(source, rect, ui.FillBox)
				return
			}
		}
		// An unresolved packaged asset stays a bounded placeholder; the ABI
		// never loads a remote URL.
		if w > 0 && h > 0 {
			p.Stroke(rect, tokenColor(c, domainnativeui.TokenBorder), 2, 1)
		}
	}
}

func canvasFill(shape *domainnativeui.Node) domainnativeui.Token {
	if value, ok := shape.Prop(domainnativeui.PropFillToken); ok && value.Kind == domainnativeui.PropInt {
		return domainnativeui.Token(value.Int)
	}
	return domainnativeui.TokenSurface
}

func canvasStroke(shape *domainnativeui.Node, def domainnativeui.Token) domainnativeui.Token {
	if value, ok := shape.Prop(domainnativeui.PropStrokeToken); ok && value.Kind == domainnativeui.PropInt {
		return domainnativeui.Token(value.Int)
	}
	if value, ok := shape.Prop(domainnativeui.PropTextToken); ok && value.Kind == domainnativeui.PropInt {
		return domainnativeui.Token(value.Int)
	}
	return def
}
