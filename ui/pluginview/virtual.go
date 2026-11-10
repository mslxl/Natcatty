package pluginview

// Virtual lists. A virtual_list declares an item count far above the item
// nodes present in one patch. MyGo's virtualizing List builds only the
// visible rows; a visible row with no item node is reported once as an
// item_request whose Index is the first missing item and Width is how many
// visible items are missing. The guest answers with a small patch of
// virtual_item nodes keyed by their absolute item_index, so a huge list never
// crosses the ABI whole and the retained tree stays bounded.

import (
	"github.com/egoist/mygo/ui"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
)

// virtualRowHeight is the fixed height of a virtual row, whether supplied or
// still loading. A stable row height keeps the visible window and therefore
// the item request small; with zero-height placeholders MyGo would build to
// its own per-frame row cap before the first patch arrives.
const virtualRowHeight = 36

// virtualState is the host-owned place, selection, and paging state of one
// virtual list.
type virtualState struct {
	list *ui.ListState

	request windowRequest
}

func (s *Surface) virtualState(node *domainnativeui.Node) *virtualState {
	state := s.virtuals[node.ID]
	if state == nil {
		selected := -1
		state = &virtualState{list: &ui.ListState{Selected: &selected}}
		s.virtuals[node.ID] = state
	}
	return state
}

// virtualList renders the visible window and pulls missing items. The item
// count may be arbitrarily large; only supplied item nodes exist in memory.
func (s *Surface) virtualList(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	state := s.virtualState(node)
	s.syncListChoice(node, state.list)
	previous := -1
	if state.list.Selected != nil {
		previous = *state.list.Selected
	}

	items := make(map[int]domainnativeui.NodeID, len(node.Children))
	highest := -1
	for _, childID := range node.Children {
		child, ok := tree.Nodes[childID]
		if !ok || child == nil || child.Kind != domainnativeui.NodeVirtualItem {
			continue
		}
		index := int(child.IntProp(domainnativeui.PropItemIndex, -1))
		if index < 0 {
			continue
		}
		items[index] = childID
		if index > highest {
			highest = index
		}
	}
	count := int(node.IntProp(domainnativeui.PropItemCount, 0))
	if count <= highest {
		count = highest + 1
	}
	if count < 0 {
		count = 0
	}
	disabled := node.BoolProp(domainnativeui.PropDisabled, false)

	firstMissing, missingCount := -1, 0
	element := ui.List(c, state.list, count, func(row int) {
		childID, ok := items[row]
		if !ok {
			if firstMissing < 0 {
				firstMissing = row
			}
			missingCount++
			// A loading row keeps the same height as a supplied one, so
			// the window does not grow and the request stays small.
			ui.Box(c).Height(virtualRowHeight)
			return
		}
		item, ok := tree.Nodes[childID]
		if !ok || item == nil || item.BoolProp(domainnativeui.PropHidden, false) {
			ui.Box(c).Height(virtualRowHeight)
			return
		}
		rowElement := s.renderNode(c, tree, childID)
		if disabled {
			return
		}
		rowElement = rowElement.Drag(listDrag{Node: node.ID, Row: row})
		if rowElement.Clicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventClick, Index: int32(row)})
			if state.list.Selected != nil {
				*state.list.Selected = row
			}
		}
		if rowElement.DoubleClicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventDoubleClick, Index: int32(row)})
		}
		if rowElement.RightClicked() {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventContextMenu, Index: int32(row)})
		}
		if rowElement.Hovered() {
			s.emitHover(node.ID)
		}
		if _, _, ok := rowElement.Dragged(); ok {
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventDrag, Index: int32(row)})
		}
	})
	if disabled {
		element = element.Disabled(true)
	}
	// A scroll or a new item window changes the element but not the selected
	// row; only a selection that actually changed is reported.
	if state.list.Selected != nil && *state.list.Selected != previous {
		s.emit(node.ID, domainnativeui.Event{
			Kind: domainnativeui.EventSelection, Index: int32(*state.list.Selected),
		})
	}
	if source, ok := ui.Drop[listDrag](element); ok {
		s.emit(node.ID, domainnativeui.Event{
			Kind: domainnativeui.EventDrop, Index: int32(source.Row),
			Related: domainnativeui.IDFromNode(source.Node),
		})
	}
	if element.Hovered() {
		s.emitHover(node.ID)
	}

	// Ask for the first missing visible window, once per window. The event
	// queue coalesces it, and the request only changes when the window does,
	// so scrolling a million-row list cannot flood the guest.
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
	return s.bindScroll(node, element)
}

// virtualItem renders one supplied item. A plain item shows its text; an item
// with children renders its own row content.
func (s *Surface) virtualItem(c *ui.Context, tree *domainnativeui.Tree,
	node *domainnativeui.Node) ui.Element {
	if len(node.Children) == 0 {
		return ui.Text(c, node.Text).Height(virtualRowHeight)
	}
	children := append([]domainnativeui.NodeID(nil), node.Children...)
	element := ui.Row(c).Gap(8).Height(virtualRowHeight)
	if size := node.FloatProp(domainnativeui.PropFontSize, 0); size > 0 {
		element = element.FontSize(float32(size))
	}
	return element.Children(func() {
		for _, child := range children {
			s.renderNode(c, tree, child)
		}
	})
}
