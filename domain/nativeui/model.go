// Package nativeui is the host-side model of the native UI ABI: the retained
// node tree, atomic patch batches, the closed event set, focus order, and the
// binary wire encoding.
//
// The package is pure: it imports only the standard library, so both the
// plugin application context and the UI renderer can share it without
// crossing an architecture boundary. Guest buffers are never retained; every
// decoder copies strings out of the input slice.
package nativeui

// NodeID is the guest-allocated identity of one retained node. It is unique
// among live nodes and is never reused until the node is destroyed.
type NodeID uint32

// NodeRef is a reference to a node in a patch batch. The high 32 bits are
// zero for a guest-allocated NodeID and nonzero for a generation-tagged node
// handle, whose low 32 bits name the table slot. The two forms let a batch
// reference a node it creates before the host has returned the handle.
type NodeRef uint64

// IDFromNode returns the ref of a guest-allocated node id.
func IDFromNode(id NodeID) NodeRef { return NodeRef(id) }

// HandleFromNode returns the ref of a generation-tagged handle.
func HandleFromNode(handle uint64) NodeRef { return NodeRef(handle) }

// IsHandle reports whether the ref is a handle rather than a guest id.
func (r NodeRef) IsHandle() bool { return r>>32 != 0 }

// NodeID returns the guest id encoded in a ref, and false for a handle.
func (r NodeRef) NodeID() (NodeID, bool) {
	if r.IsHandle() {
		return 0, false
	}
	return NodeID(r), true
}

// Handle returns the handle encoded in a ref, and false for a guest id.
func (r NodeRef) Handle() (uint64, bool) {
	if !r.IsHandle() {
		return 0, false
	}
	return uint64(r), true
}

// NodeKind is the closed v1 core widget set. Values are part of the wire
// contract and must match the generated IDL bindings.
type NodeKind int32

const (
	NodeContainer NodeKind = 0
	NodeText      NodeKind = 1
	NodeButton    NodeKind = 2
	NodeInput     NodeKind = 3
	NodeCheckbox  NodeKind = 4
	NodeRadio     NodeKind = 5
	NodeSelect    NodeKind = 6
	NodeSlider    NodeKind = 7
	NodeProgress  NodeKind = 8
	NodeBadge     NodeKind = 9
	NodeDivider   NodeKind = 10
	NodeImage     NodeKind = 11
	NodeList      NodeKind = 12
	NodeTable     NodeKind = 13
	NodeTree      NodeKind = 14
	NodeTabs      NodeKind = 15
	NodeTooltip   NodeKind = 16
	NodeDialog    NodeKind = 17
	NodeMenu      NodeKind = 18
	NodeToast     NodeKind = 19
	// NodeDataGrid and its family: a column, a sparse window of rows keyed by
	// their absolute display index, and the grid itself. The grid's row count
	// may be far larger than the rows present in one patch.
	NodeDataGrid   NodeKind = 20
	NodeDataColumn NodeKind = 21
	NodeDataRow    NodeKind = 22
	// NodeGraph, its nodes and its edges. Positions are host-side layout in
	// logical DIPs; edges reference graph nodes with PropFrom and PropTo.
	NodeGraph     NodeKind = 23
	NodeGraphNode NodeKind = 24
	NodeGraphEdge NodeKind = 25
	// NodePlanGraph and its plan tree. A plan node carries a status, cost,
	// annotation, and expansion state; annotations are display-only.
	NodePlanGraph NodeKind = 26
	NodePlanNode  NodeKind = 27
	// NodeCanvas and its drawing primitives. CanvasShape children draw rects,
	// lines, ellipses, text, and packaged images in the canvas's own DIPs.
	NodeCanvas      NodeKind = 28
	NodeCanvasShape NodeKind = 29
	// Rich content added in ABI 1.9.0. A rich_text paragraph contains
	// text_span children, each with its own style and an optional link. A
	// virtual_list declares an item count far above the items present in one
	// patch and pulls the visible window through item_request; every
	// virtual_item names its absolute display index.
	NodeRichText    NodeKind = 30
	NodeTextSpan    NodeKind = 31
	NodeVirtualList NodeKind = 32
	NodeVirtualItem NodeKind = 33
	// NodeWebview embeds the per-plugin sandboxed page inside a native view.
	// Its src names a packaged HTML entry under ui/; the node itself has no
	// children and the host owns its size, focus, visibility, and IME.
	NodeWebview NodeKind = 34
)

// Valid reports whether k is a core widget kind.
func (k NodeKind) Valid() bool { return k >= NodeContainer && k <= NodeWebview }

// String renders the IDL name of k.
func (k NodeKind) String() string {
	switch k {
	case NodeContainer:
		return "container"
	case NodeText:
		return "text"
	case NodeButton:
		return "button"
	case NodeInput:
		return "input"
	case NodeCheckbox:
		return "checkbox"
	case NodeRadio:
		return "radio"
	case NodeSelect:
		return "select"
	case NodeSlider:
		return "slider"
	case NodeProgress:
		return "progress"
	case NodeBadge:
		return "badge"
	case NodeDivider:
		return "divider"
	case NodeImage:
		return "image"
	case NodeList:
		return "list"
	case NodeTable:
		return "table"
	case NodeTree:
		return "tree"
	case NodeTabs:
		return "tabs"
	case NodeTooltip:
		return "tooltip"
	case NodeDialog:
		return "dialog"
	case NodeMenu:
		return "menu"
	case NodeToast:
		return "toast"
	case NodeDataGrid:
		return "data_grid"
	case NodeDataColumn:
		return "data_column"
	case NodeDataRow:
		return "data_row"
	case NodeGraph:
		return "graph"
	case NodeGraphNode:
		return "graph_node"
	case NodeGraphEdge:
		return "graph_edge"
	case NodePlanGraph:
		return "plan_graph"
	case NodePlanNode:
		return "plan_node"
	case NodeCanvas:
		return "canvas"
	case NodeCanvasShape:
		return "canvas_shape"
	case NodeRichText:
		return "rich_text"
	case NodeTextSpan:
		return "text_span"
	case NodeVirtualList:
		return "virtual_list"
	case NodeVirtualItem:
		return "virtual_item"
	case NodeWebview:
		return "webview"
	}
	return "unknown"
}

// PropID is a closed, per-kind property identifier. Values are part of the
// wire contract and must match the generated IDL bindings.
type PropID int32

const (
	PropLayout          PropID = 1
	PropGap             PropID = 2
	PropPaddingStart    PropID = 3
	PropPaddingEnd      PropID = 4
	PropPaddingTop      PropID = 5
	PropPaddingBottom   PropID = 6
	PropMarginStart     PropID = 7
	PropMarginEnd       PropID = 8
	PropMarginTop       PropID = 9
	PropMarginBottom    PropID = 10
	PropWidth           PropID = 11
	PropHeight          PropID = 12
	PropGrow            PropID = 13
	PropHidden          PropID = 14
	PropDisabled        PropID = 15
	PropFocusable       PropID = 16
	PropTabIndex        PropID = 17
	PropLabel           PropID = 18
	PropRole            PropID = 19
	PropLabelledBy      PropID = 20
	PropKeyCapture      PropID = 21
	PropValue           PropID = 22
	PropPlaceholder     PropID = 23
	PropReadOnly        PropID = 24
	PropMultiline       PropID = 25
	PropChecked         PropID = 26
	PropSelected        PropID = 27
	PropMin             PropID = 28
	PropMax             PropID = 29
	PropTooltip         PropID = 30
	PropBackgroundToken PropID = 31
	PropTextToken       PropID = 32
	PropBorderToken     PropID = 33
	PropRadius          PropID = 34
	PropFontSize        PropID = 35
	PropFontWeight      PropID = 36
	PropOpacity         PropID = 37
	PropSrc             PropID = 38
	PropAlt             PropID = 39
	PropColumns         PropID = 40
	PropExpanded        PropID = 41
	// Data-surface props added in ABI 1.8.0. They are additive: every
	// earlier id keeps its numeric value.
	PropSortColumn     PropID = 42
	PropSortDescending PropID = 43
	PropRowCount       PropID = 44
	PropRowIndex       PropID = 45
	PropSortable       PropID = 46
	PropFrom           PropID = 47
	PropTo             PropID = 48
	PropDirected       PropID = 49
	PropX              PropID = 50
	PropY              PropID = 51
	PropStrokeToken    PropID = 52
	PropFillToken      PropID = 53
	PropLineWidth      PropID = 54
	PropShape          PropID = 55
	PropAnnotation     PropID = 56
	PropCost           PropID = 57
	PropPlanStatus     PropID = 58
	// Rich-content props added in ABI 1.9.0. Font and image values name a
	// packaged asset id; a remote URL or an escaping path is rejected.
	PropLink           PropID = 59
	PropItalic         PropID = 60
	PropUnderline      PropID = 61
	PropStrikethrough  PropID = 62
	PropFontAsset      PropID = 63
	PropItemCount      PropID = 64
	PropItemIndex      PropID = 65
	PropTransitionMs   PropID = 66
	PropTransitionEase PropID = 67
)

// Ease is the closed easing vocabulary of a host-driven property transition.
// Values are part of the wire contract and must match the generated IDL
// bindings.
type Ease int32

const (
	EaseLinear Ease = 0
	EaseIn     Ease = 1
	EaseOut    Ease = 2
	EaseInOut  Ease = 3
)

// Valid reports whether e is a declared easing.
func (e Ease) Valid() bool { return e >= EaseLinear && e <= EaseInOut }

// MaxTransitionMs bounds one host-driven property transition. A longer value
// is rejected rather than letting an animation outlive its usefulness.
const MaxTransitionMs = 60000

// MaxAssetIDBytes bounds one packaged asset id.
const MaxAssetIDBytes = 256

// CanvasShape is the closed set of canvas drawing primitives. Values are part
// of the wire contract and must match the generated IDL bindings.
type CanvasShape int32

const (
	CanvasRect    CanvasShape = 0
	CanvasLine    CanvasShape = 1
	CanvasEllipse CanvasShape = 2
	CanvasText    CanvasShape = 3
	CanvasImage   CanvasShape = 4
)

// Valid reports whether s is a declared canvas shape.
func (s CanvasShape) Valid() bool { return s >= CanvasRect && s <= CanvasImage }

// PlanStatus is the closed status vocabulary of one plan node.
type PlanStatus int32

const (
	PlanNone    PlanStatus = 0
	PlanRunning PlanStatus = 1
	PlanDone    PlanStatus = 2
	PlanFailed  PlanStatus = 3
	PlanSkipped PlanStatus = 4
)

// Valid reports whether s is a declared plan status.
func (s PlanStatus) Valid() bool { return s >= PlanNone && s <= PlanSkipped }

// PropValueKind is the closed type tag of a property value.
type PropValueKind uint32

const (
	PropBool   PropValueKind = 0
	PropInt    PropValueKind = 1
	PropFloat  PropValueKind = 2
	PropString PropValueKind = 3
	PropRef    PropValueKind = 4
)

// Value is one typed property value. Exactly the field named by Kind is
// meaningful.
type Value struct {
	Kind  PropValueKind
	Int   int64
	Float float64
	Text  string
	Ref   NodeRef
}

// BoolValue returns a boolean property value.
func BoolValue(v bool) Value {
	if v {
		return Value{Kind: PropBool, Int: 1}
	}
	return Value{Kind: PropBool}
}

// IntValue returns an integer property value.
func IntValue(v int64) Value { return Value{Kind: PropInt, Int: v} }

// FloatValue returns a floating-point property value.
func FloatValue(v float64) Value { return Value{Kind: PropFloat, Float: v} }

// StringValue returns a string property value.
func StringValue(v string) Value { return Value{Kind: PropString, Text: v} }

// RefValue returns a node reference property value.
func RefValue(v NodeRef) Value { return Value{Kind: PropRef, Ref: v} }

// propSpec is the closed type of one property and the kinds that accept it.
type propSpec struct {
	kind  PropValueKind
	kinds []NodeKind
}

// coreWidgets is the ticket 11 widget set. The data-oriented kinds extend
// these groups rather than repeating every generic property.
var coreWidgets = []NodeKind{
	NodeContainer, NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio,
	NodeSelect, NodeSlider, NodeProgress, NodeBadge, NodeDivider, NodeImage,
	NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu,
	NodeToast,
}

// plus returns base with extra appended, without sharing base's backing array.
func plus(base []NodeKind, extra ...NodeKind) []NodeKind {
	out := make([]NodeKind, 0, len(base)+len(extra))
	out = append(out, base...)
	return append(out, extra...)
}

// animatableKinds are the independently rendered widgets a host-driven
// property transition may move. Kinds that only render inside a parent are
// excluded: their parent owns their place and size.
var animatableKinds = plus(coreWidgets, NodeDataGrid, NodePlanGraph,
	NodeCanvas, NodeRichText, NodeVirtualList, NodeWebview)

// fontKinds are the widgets whose text accepts a packaged font family.
var fontKinds = plus([]NodeKind{NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio,
	NodeSelect, NodeSlider, NodeProgress, NodeBadge, NodeList, NodeTable, NodeTree,
	NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast},
	NodeDataGrid, NodeDataColumn, NodeGraph, NodeGraphNode, NodePlanGraph,
	NodePlanNode, NodeCanvasShape, NodeRichText, NodeTextSpan, NodeVirtualItem)

// propSpecs is the closed property table. Unknown property ids and
// properties outside a kind's list are rejected.
var propSpecs = map[PropID]propSpec{
	PropLayout: {kind: PropInt, kinds: plus([]NodeKind{NodeContainer, NodeList, NodeTable, NodeTree, NodeTabs, NodeDialog, NodeMenu, NodeToast, NodeTooltip, NodeVirtualList}, NodeDataGrid, NodeGraph, NodePlanGraph)},
	PropGap:    {kind: PropFloat, kinds: plus([]NodeKind{NodeContainer, NodeList, NodeTable, NodeTree, NodeTabs, NodeDialog, NodeMenu, NodeToast, NodeVirtualList}, NodeDataGrid, NodePlanGraph)},

	PropPaddingStart:  {kind: PropFloat, kinds: plus([]NodeKind{NodeContainer, NodeList, NodeTable, NodeTree, NodeTabs, NodeDialog, NodeMenu, NodeToast, NodeTooltip, NodeRichText, NodeVirtualList}, NodeDataGrid, NodeGraph, NodePlanGraph, NodeCanvas, NodeWebview)},
	PropPaddingEnd:    {kind: PropFloat, kinds: plus([]NodeKind{NodeContainer, NodeList, NodeTable, NodeTree, NodeTabs, NodeDialog, NodeMenu, NodeToast, NodeTooltip, NodeRichText, NodeVirtualList}, NodeDataGrid, NodeGraph, NodePlanGraph, NodeCanvas, NodeWebview)},
	PropPaddingTop:    {kind: PropFloat, kinds: plus([]NodeKind{NodeContainer, NodeList, NodeTable, NodeTree, NodeTabs, NodeDialog, NodeMenu, NodeToast, NodeTooltip, NodeRichText, NodeVirtualList}, NodeDataGrid, NodeGraph, NodePlanGraph, NodeCanvas, NodeWebview)},
	PropPaddingBottom: {kind: PropFloat, kinds: plus([]NodeKind{NodeContainer, NodeList, NodeTable, NodeTree, NodeTabs, NodeDialog, NodeMenu, NodeToast, NodeTooltip, NodeRichText, NodeVirtualList}, NodeDataGrid, NodeGraph, NodePlanGraph, NodeCanvas, NodeWebview)},

	PropMarginStart:  {kind: PropFloat, kinds: plus(coreWidgets, NodeDataGrid, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas, NodeRichText, NodeVirtualList, NodeWebview)},
	PropMarginEnd:    {kind: PropFloat, kinds: plus(coreWidgets, NodeDataGrid, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas, NodeRichText, NodeVirtualList, NodeWebview)},
	PropMarginTop:    {kind: PropFloat, kinds: plus(coreWidgets, NodeDataGrid, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas, NodeRichText, NodeVirtualList, NodeWebview)},
	PropMarginBottom: {kind: PropFloat, kinds: plus(coreWidgets, NodeDataGrid, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas, NodeRichText, NodeVirtualList, NodeWebview)},

	PropWidth:  {kind: PropFloat, kinds: plus(coreWidgets, NodeDataGrid, NodeDataColumn, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas, NodeCanvasShape, NodeRichText, NodeVirtualList, NodeWebview)},
	PropHeight: {kind: PropFloat, kinds: plus(coreWidgets, NodeDataGrid, NodeDataColumn, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas, NodeCanvasShape, NodeRichText, NodeVirtualList, NodeWebview)},
	PropGrow:   {kind: PropFloat, kinds: plus(coreWidgets, NodeDataGrid, NodeGraph, NodePlanGraph, NodeCanvas, NodeRichText, NodeVirtualList, NodeWebview)},

	PropHidden:   {kind: PropBool, kinds: plus(coreWidgets, NodeDataGrid, NodeDataColumn, NodeDataRow, NodeGraph, NodeGraphNode, NodeGraphEdge, NodePlanGraph, NodePlanNode, NodeCanvas, NodeCanvasShape, NodeRichText, NodeTextSpan, NodeVirtualList, NodeVirtualItem, NodeWebview)},
	PropDisabled: {kind: PropBool, kinds: plus(coreWidgets, NodeDataGrid, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas, NodeVirtualList, NodeWebview)},

	PropFocusable: {kind: PropBool, kinds: plus([]NodeKind{NodeContainer, NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeVirtualList}, NodeDataGrid, NodeGraph, NodePlanGraph, NodeCanvas, NodeWebview)},
	PropTabIndex:  {kind: PropInt, kinds: plus([]NodeKind{NodeContainer, NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeVirtualList}, NodeDataGrid, NodeGraph, NodePlanGraph, NodeCanvas, NodeWebview)},

	PropLabel:      {kind: PropString, kinds: plus([]NodeKind{NodeContainer, NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeProgress, NodeBadge, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeRichText, NodeVirtualList, NodeVirtualItem}, NodeDataGrid, NodeDataColumn, NodeGraph, NodeGraphNode, NodeGraphEdge, NodePlanGraph, NodePlanNode, NodeCanvas)},
	PropRole:       {kind: PropInt, kinds: plus([]NodeKind{NodeContainer, NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeProgress, NodeBadge, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeRichText, NodeVirtualList, NodeVirtualItem}, NodeDataGrid, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas)},
	PropLabelledBy: {kind: PropRef, kinds: plus([]NodeKind{NodeContainer, NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeProgress, NodeBadge, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeRichText, NodeVirtualList}, NodeDataGrid, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas)},

	PropKeyCapture:  {kind: PropBool, kinds: plus([]NodeKind{NodeContainer, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeList, NodeTable, NodeTree, NodeTabs, NodeDialog, NodeMenu, NodeVirtualList}, NodeDataGrid, NodeGraph, NodePlanGraph)},
	PropValue:       {kind: PropString, kinds: []NodeKind{NodeInput, NodeSelect, NodeSlider, NodeProgress}},
	PropPlaceholder: {kind: PropString, kinds: []NodeKind{NodeInput}},
	PropReadOnly:    {kind: PropBool, kinds: []NodeKind{NodeInput}},
	PropMultiline:   {kind: PropBool, kinds: []NodeKind{NodeInput}},
	PropChecked:     {kind: PropBool, kinds: []NodeKind{NodeCheckbox, NodeRadio, NodeTabs}},
	PropSelected:    {kind: PropInt, kinds: plus([]NodeKind{NodeSelect, NodeTabs, NodeList, NodeTable, NodeTree, NodeDataGrid, NodeVirtualList}, NodeGraphNode)},
	PropMin:         {kind: PropFloat, kinds: []NodeKind{NodeSlider, NodeProgress}},
	PropMax:         {kind: PropFloat, kinds: []NodeKind{NodeSlider, NodeProgress}},

	PropTooltip: {kind: PropString, kinds: plus([]NodeKind{NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeBadge, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeMenu, NodeRichText, NodeVirtualList, NodeVirtualItem}, NodeDataGrid, NodeGraph, NodeGraphNode, NodeGraphEdge, NodePlanGraph, NodePlanNode, NodeCanvas)},

	PropBackgroundToken: {kind: PropInt, kinds: plus([]NodeKind{NodeContainer, NodeBadge, NodeDivider, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeRichText, NodeVirtualList, NodeVirtualItem}, NodeDataGrid, NodeDataColumn, NodeDataRow, NodeGraph, NodeGraphNode, NodePlanGraph, NodePlanNode, NodeCanvas)},
	PropTextToken:       {kind: PropInt, kinds: plus([]NodeKind{NodeContainer, NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeProgress, NodeBadge, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeRichText, NodeTextSpan, NodeVirtualList, NodeVirtualItem}, NodeDataGrid, NodeDataColumn, NodeDataRow, NodeGraph, NodeGraphNode, NodeGraphEdge, NodePlanGraph, NodePlanNode, NodeCanvas, NodeCanvasShape)},
	PropBorderToken:     {kind: PropInt, kinds: plus([]NodeKind{NodeContainer, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeBadge, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeRichText, NodeVirtualList}, NodeDataGrid, NodeDataColumn, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas)},
	PropRadius:          {kind: PropFloat, kinds: plus([]NodeKind{NodeContainer, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeBadge, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeRichText, NodeVirtualList}, NodeDataGrid, NodeGraph, NodeGraphNode, NodePlanGraph, NodeCanvas)},
	PropFontSize:        {kind: PropFloat, kinds: fontKinds},
	PropFontWeight:      {kind: PropInt, kinds: fontKinds},
	PropOpacity:         {kind: PropFloat, kinds: plus([]NodeKind{NodeContainer, NodeText, NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider, NodeProgress, NodeBadge, NodeDivider, NodeImage, NodeList, NodeTable, NodeTree, NodeTabs, NodeTooltip, NodeDialog, NodeMenu, NodeToast, NodeRichText, NodeTextSpan, NodeVirtualList, NodeVirtualItem}, NodeDataGrid, NodeDataColumn, NodeDataRow, NodeGraph, NodeGraphNode, NodePlanGraph, NodePlanNode, NodeCanvas, NodeCanvasShape)},

	PropSrc:      {kind: PropString, kinds: []NodeKind{NodeImage, NodeCanvasShape, NodeWebview}},
	PropAlt:      {kind: PropString, kinds: []NodeKind{NodeImage, NodeCanvasShape}},
	PropColumns:  {kind: PropInt, kinds: []NodeKind{NodeTable}},
	PropExpanded: {kind: PropBool, kinds: []NodeKind{NodeTree, NodePlanNode}},

	// Data-oriented props added in ABI 1.8.0.
	PropSortColumn:     {kind: PropInt, kinds: []NodeKind{NodeDataGrid}},
	PropSortDescending: {kind: PropBool, kinds: []NodeKind{NodeDataGrid}},
	PropRowCount:       {kind: PropInt, kinds: []NodeKind{NodeDataGrid}},
	PropRowIndex:       {kind: PropInt, kinds: []NodeKind{NodeDataRow}},
	PropSortable:       {kind: PropBool, kinds: []NodeKind{NodeDataColumn}},
	PropFrom:           {kind: PropRef, kinds: []NodeKind{NodeGraphEdge}},
	PropTo:             {kind: PropRef, kinds: []NodeKind{NodeGraphEdge}},
	PropDirected:       {kind: PropBool, kinds: []NodeKind{NodeGraphEdge}},
	PropX:              {kind: PropFloat, kinds: []NodeKind{NodeGraphNode, NodeCanvasShape}},
	PropY:              {kind: PropFloat, kinds: []NodeKind{NodeGraphNode, NodeCanvasShape}},
	PropStrokeToken:    {kind: PropInt, kinds: []NodeKind{NodeGraphEdge, NodeCanvasShape}},
	PropFillToken:      {kind: PropInt, kinds: []NodeKind{NodeCanvasShape}},
	PropLineWidth:      {kind: PropFloat, kinds: []NodeKind{NodeGraphEdge, NodeCanvasShape}},
	PropShape:          {kind: PropInt, kinds: []NodeKind{NodeCanvasShape}},
	PropAnnotation:     {kind: PropString, kinds: []NodeKind{NodePlanNode}},
	PropCost:           {kind: PropFloat, kinds: []NodeKind{NodePlanNode}},
	PropPlanStatus:     {kind: PropInt, kinds: []NodeKind{NodePlanNode}},

	// Rich-content props added in ABI 1.9.0.
	PropLink:           {kind: PropString, kinds: []NodeKind{NodeTextSpan}},
	PropItalic:         {kind: PropBool, kinds: []NodeKind{NodeRichText, NodeTextSpan}},
	PropUnderline:      {kind: PropBool, kinds: []NodeKind{NodeRichText, NodeTextSpan}},
	PropStrikethrough:  {kind: PropBool, kinds: []NodeKind{NodeRichText, NodeTextSpan}},
	PropFontAsset:      {kind: PropString, kinds: fontKinds},
	PropItemCount:      {kind: PropInt, kinds: []NodeKind{NodeVirtualList}},
	PropItemIndex:      {kind: PropInt, kinds: []NodeKind{NodeVirtualItem}},
	PropTransitionMs:   {kind: PropInt, kinds: animatableKinds},
	PropTransitionEase: {kind: PropInt, kinds: animatableKinds},
}

// Role is the accessibility role a node declares. Values are part of the
// wire contract and must match the generated IDL bindings.
type Role int32

const (
	RoleNone      Role = 0
	RoleButton    Role = 1
	RoleText      Role = 2
	RoleHeading   Role = 3
	RoleTextField Role = 4
	RoleCheckbox  Role = 5
	RoleRadio     Role = 6
	RoleSlider    Role = 7
	RoleProgress  Role = 8
	RoleList      Role = 9
	RoleListItem  Role = 10
	RoleTable     Role = 11
	RoleTree      Role = 12
	RoleTab       Role = 13
	RoleTabList   Role = 14
	RoleDialog    Role = 15
	RoleMenu      Role = 16
	RoleMenuItem  Role = 17
	RoleImage     Role = 18
	RoleTooltip   Role = 19
	RoleStatus    Role = 20
)

// Valid reports whether r is a declared role.
func (r Role) Valid() bool { return r >= RoleNone && r <= RoleStatus }

// Token is a semantic theme token. Values are part of the wire contract and
// must match the generated IDL bindings.
type Token int32

const (
	TokenBackground     Token = 0
	TokenSurface        Token = 1
	TokenSurfaceHover   Token = 2
	TokenSurfacePressed Token = 3
	TokenBorder         Token = 4
	TokenText           Token = 5
	TokenTextMuted      Token = 6
	TokenAccent         Token = 7
	TokenAccentText     Token = 8
	TokenDanger         Token = 9
	TokenWarning        Token = 10
	TokenSuccess        Token = 11
	TokenSelection      Token = 12
	TokenFocus          Token = 13
	TokenInverse        Token = 14
	TokenInverseText    Token = 15
)

// Valid reports whether t is a declared semantic token.
func (t Token) Valid() bool { return t >= TokenBackground && t <= TokenInverseText }

// Layout is the direction a container lays its children out in.
type Layout int32

const (
	LayoutRow    Layout = 0
	LayoutColumn Layout = 1
	LayoutBox    Layout = 2
	LayoutGrid   Layout = 3
)

// Valid reports whether l is a declared layout.
func (l Layout) Valid() bool { return l >= LayoutRow && l <= LayoutGrid }

// Node is one retained UI node. It is immutable once committed: a batch
// replaces nodes rather than mutating live ones, so renderers may read a
// snapshot without locking.
type Node struct {
	ID       NodeID
	Kind     NodeKind
	Props    map[PropID]Value
	Text     string
	Children []NodeID
	Parent   NodeID
}

// Prop returns a property value.
func (n *Node) Prop(id PropID) (Value, bool) {
	if n == nil {
		return Value{}, false
	}
	value, ok := n.Props[id]
	return value, ok
}

// BoolProp returns a boolean property, or def when unset.
func (n *Node) BoolProp(id PropID, def bool) bool {
	if value, ok := n.Prop(id); ok && value.Kind == PropBool {
		return value.Int != 0
	}
	return def
}

// IntProp returns an integer property, or def when unset.
func (n *Node) IntProp(id PropID, def int64) int64 {
	if value, ok := n.Prop(id); ok && value.Kind == PropInt {
		return value.Int
	}
	return def
}

// FloatProp returns a floating-point property, or def when unset.
func (n *Node) FloatProp(id PropID, def float64) float64 {
	if value, ok := n.Prop(id); ok && value.Kind == PropFloat {
		return value.Float
	}
	return def
}

// StringProp returns a string property, or "" when unset.
func (n *Node) StringProp(id PropID) string {
	if value, ok := n.Prop(id); ok && value.Kind == PropString {
		return value.Text
	}
	return ""
}

// Interactive reports whether the kind takes input and therefore requires an
// accessible label or label reference.
func (k NodeKind) Interactive() bool {
	switch k {
	case NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider,
		NodeTabs, NodeMenu, NodeDialog, NodeDataGrid, NodeGraph, NodePlanGraph,
		NodeCanvas, NodeVirtualList:
		return true
	}
	return false
}

// FocusableKind reports whether the kind may take keyboard focus.
func (k NodeKind) FocusableKind() bool {
	switch k {
	case NodeButton, NodeInput, NodeCheckbox, NodeRadio, NodeSelect, NodeSlider,
		NodeTabs, NodeList, NodeTable, NodeTree, NodeMenu, NodeDataGrid,
		NodeGraph, NodePlanGraph, NodeCanvas, NodeVirtualList, NodeWebview:
		return true
	}
	return false
}

// TextKind reports whether set_text has a visible effect.
func (k NodeKind) TextKind() bool {
	switch k {
	case NodeText, NodeBadge, NodeTooltip, NodeToast, NodeDialog, NodeGraphNode,
		NodePlanNode, NodeCanvasShape, NodeRichText, NodeTextSpan, NodeVirtualItem:
		return true
	}
	return false
}
