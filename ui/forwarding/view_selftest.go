//go:build selftest

package forwarding

import "github.com/egoist/mygo/ui"

// SelftestEditorField identifies one text input in the forwarding editor.
type SelftestEditorField int

const (
	SelftestEditorLabel SelftestEditorField = iota
	SelftestEditorBindAddress
	SelftestEditorLocalPort
	SelftestEditorRemoteHost
	SelftestEditorRemotePort
)

// SelftestEditorFieldBounds reports the box of field in c's coordinates, as of
// the previous frame. It exists only in selftest builds, so the shell
// self-test can prove the responsive rows keep every input inside the real
// client at 704x421 without widening the production API. It is the zero rect
// before the editor has laid the field out.
func SelftestEditorFieldBounds(s *State, c *ui.Context, field SelftestEditorField) ui.Rect {
	if s == nil || c == nil {
		return ui.Rect{}
	}
	switch field {
	case SelftestEditorLabel:
		return s.editorFields.label.Bounds(c)
	case SelftestEditorBindAddress:
		return s.editorFields.bindAddress.Bounds(c)
	case SelftestEditorLocalPort:
		return s.editorFields.localPort.Bounds(c)
	case SelftestEditorRemoteHost:
		return s.editorFields.remoteHost.Bounds(c)
	case SelftestEditorRemotePort:
		return s.editorFields.remotePort.Bounds(c)
	default:
		return ui.Rect{}
	}
}
