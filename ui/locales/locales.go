// Package locales holds Netcatty's ARB/ICU message catalogs and the accessors
// mygo generate writes from them. The accessors are generated code: edit the
// JSON catalogs, then run `go tool mygo generate`.
package locales

import (
	"strings"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Tags returns the languages the app ships, source language first, as the
// embedded catalogs declare them. The shell and the language menu offer them
// in this order.
func Tags() []string { return Catalog.Languages() }

// EncodingLabel returns the selector name for encoding. Encoding names are
// IANA identifiers, so every catalog spells them the same way, but they are
// still display strings and therefore live in the catalogs.
func EncodingLabel(msgs Messages, encoding domainterminal.Encoding) string {
	switch domainterminal.NormalizeEncoding(encoding) {
	case domainterminal.EncodingGB18030:
		return msgs.SessionEncodingGB18030()
	case domainterminal.EncodingShiftJIS:
		return msgs.SessionEncodingShiftJIS()
	case domainterminal.EncodingLatin1:
		return msgs.SessionEncodingLatin1()
	default:
		return msgs.SessionEncodingUTF8()
	}
}

// commandNames lists the common commands the autocomplete popup describes.
// CommandDescription's switch and this list are kept together: the command
// catalog test fails when a name here has no description, so a catalog key and
// its accessor cannot silently go missing.
var commandNames = []string{
	"ls", "cd", "pwd", "cat", "cp", "mv", "rm", "mkdir", "find", "grep",
	"sed", "awk", "sort", "head", "tail", "chmod", "ps", "kill", "df", "tar",
	"ssh", "curl", "git", "make", "sudo", "man", "vim", "echo", "docker",
	"systemctl",
}

// CommandDescription returns the one-line description of the first word of a
// command line, or "" when the catalog has none. A directory prefix is
// dropped, so "/usr/bin/git status" is described as git. The descriptions live
// in the same JSON catalogs as the rest of the UI, so the shipped locales are
// checked by the catalog parity test and an unknown locale falls back to
// English through mygo's catalog chain.
func CommandDescription(msgs Messages, command string) string {
	switch commandName(command) {
	case "ls":
		return msgs.CommandDescriptionLs()
	case "cd":
		return msgs.CommandDescriptionCd()
	case "pwd":
		return msgs.CommandDescriptionPwd()
	case "cat":
		return msgs.CommandDescriptionCat()
	case "cp":
		return msgs.CommandDescriptionCp()
	case "mv":
		return msgs.CommandDescriptionMv()
	case "rm":
		return msgs.CommandDescriptionRm()
	case "mkdir":
		return msgs.CommandDescriptionMkdir()
	case "find":
		return msgs.CommandDescriptionFind()
	case "grep":
		return msgs.CommandDescriptionGrep()
	case "sed":
		return msgs.CommandDescriptionSed()
	case "awk":
		return msgs.CommandDescriptionAwk()
	case "sort":
		return msgs.CommandDescriptionSort()
	case "head":
		return msgs.CommandDescriptionHead()
	case "tail":
		return msgs.CommandDescriptionTail()
	case "chmod":
		return msgs.CommandDescriptionChmod()
	case "ps":
		return msgs.CommandDescriptionPs()
	case "kill":
		return msgs.CommandDescriptionKill()
	case "df":
		return msgs.CommandDescriptionDf()
	case "tar":
		return msgs.CommandDescriptionTar()
	case "ssh":
		return msgs.CommandDescriptionSsh()
	case "curl":
		return msgs.CommandDescriptionCurl()
	case "git":
		return msgs.CommandDescriptionGit()
	case "make":
		return msgs.CommandDescriptionMake()
	case "sudo":
		return msgs.CommandDescriptionSudo()
	case "man":
		return msgs.CommandDescriptionMan()
	case "vim":
		return msgs.CommandDescriptionVim()
	case "echo":
		return msgs.CommandDescriptionEcho()
	case "docker":
		return msgs.CommandDescriptionDocker()
	case "systemctl":
		return msgs.CommandDescriptionSystemctl()
	default:
		return ""
	}
}

// commandName returns the first word of a command line without a directory
// prefix, the name a shell would execute. Blank input returns "".
func commandName(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	name := fields[0]
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	return name
}
