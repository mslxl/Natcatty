package model

// TabID identifies an ordered Tab inside a Workspace. It is opaque: callers
// may compare and display it, never parse it.
type TabID string

// PaneID identifies one leaf Pane of a Workspace. A Pane displays one
// Session.
type PaneID string

// SplitID identifies a split inside a recursive split tree. It is the stable
// key a split ratio is stored under.
type SplitID string
