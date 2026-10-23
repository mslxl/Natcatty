//go:build windows

package sftp

import "golang.org/x/sys/windows"

// hiddenAttribute reports the Windows hidden attribute. Explorer and the
// original app both hide these files when hidden files are off.
func hiddenAttribute(path string) bool {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return false
	}
	return attributes&windows.FILE_ATTRIBUTE_HIDDEN != 0
}
