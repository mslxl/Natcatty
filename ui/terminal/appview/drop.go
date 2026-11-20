package appview

import (
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// A terminal accepts files dragged from Finder or Explorer: they are pasted as
// quoted paths, and a drop ripples where they landed.
//
// MyGo has two drop channels. FileDragOver/FileDrop carry paths only, and the
// macOS backend never sends them: an AppKit drag arrives as a native data drag
// (DataDragOver/DataDrop) whatever it contains. Reading only the file channel
// made every drop on a Mac invisible, so both are asked and the data channel's
// file list is decoded here.
func fileFormats() transfer.DropOptions {
	return transfer.DropOptions{Formats: []transfer.Format{transfer.FileList, transfer.URIList}}
}

// FileDragOver reports whether files are being dragged over el.
func FileDragOver(el ui.Element) bool {
	if el.FileDragOver() {
		return true
	}
	_, over := ui.DataDragOver(el, fileFormats())
	return over
}

// DroppedFiles returns the files dropped on el since the last frame, nil when
// none, and makes el take files dragged over it.
func DroppedFiles(el ui.Element) []string {
	if files := el.DroppedFiles(); len(files) > 0 {
		return files
	}
	if d, ok := ui.DropData(el, fileFormats()); ok {
		if files, err := d.Data.Files(); err == nil {
			return files
		}
	}
	return nil
}
