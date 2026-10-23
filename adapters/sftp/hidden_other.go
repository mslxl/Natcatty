//go:build !windows

package sftp

// hiddenAttribute has no portable meaning off Windows; dotfiles are hidden by
// name in the domain instead.
func hiddenAttribute(string) bool { return false }
