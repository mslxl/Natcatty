package vault

import (
	"regexp"
	"strings"
)

// snippetVariablePattern matches {{name}} and {{name:default}}. The name may
// not contain "}" or ":", so a Docker Go template action such as
// {{.LogPath}} is never mistaken for a Snippet variable.
var snippetVariablePattern = regexp.MustCompile(`\{\{([^}:]+)(?::([^}]*))?\}\}`)

// SnippetVariableDef is one variable a Snippet command declares, in first
// occurrence order.
type SnippetVariableDef struct {
	Name string
	// DefaultValue is set when the token carried a ":default" part. An empty
	// default is still a default: {{name:}} makes the variable optional.
	DefaultValue string
	HasDefault   bool
}

// SnippetVariableMatch is one variable token with its byte offset in the
// command.
type SnippetVariableMatch struct {
	Def   SnippetVariableDef
	Start int
	End   int
}

// SnippetHasVariables reports whether a command declares at least one
// variable.
func SnippetHasVariables(command string) bool {
	return len(ParseSnippetVariables(command)) > 0
}

// ParseSnippetVariables returns the variables a command declares, deduplicated
// by first occurrence. Go template actions are ignored, so a Docker
// --format='{{.LogPath}}' command declares nothing.
func ParseSnippetVariables(command string) []SnippetVariableDef {
	matches, goTemplate := collectSnippetVariableMatches(command)
	defs := make([]SnippetVariableDef, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		if goTemplate[match.Start] {
			continue
		}
		name := strings.TrimSpace(match.Def.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		def := match.Def
		def.Name = name
		defs = append(defs, def)
	}
	return defs
}

// ApplySnippetVariables substitutes every declared variable. A value that is
// empty or whitespace falls back to the token's default; a variable with
// neither is reported as missing and nothing is substituted, so a half-filled
// command never reaches a Session.
func ApplySnippetVariables(command string, values map[string]string) (string, []string) {
	defs := ParseSnippetVariables(command)
	if len(defs) == 0 {
		return command, nil
	}

	missing := make([]string, 0, len(defs))
	resolved := make(map[string]string, len(defs))
	for _, def := range defs {
		if value, ok := resolveSnippetVariableValue(def, values); ok {
			resolved[def.Name] = value
			continue
		}
		missing = append(missing, def.Name)
	}
	if len(missing) > 0 {
		return "", missing
	}
	return replaceSnippetVariableTokens(command, func(name string) string {
		return resolved[name]
	}), nil
}

// PreviewSnippetCommand renders a command with the values it has, leaving an
// unfilled required variable as its placeholder. It never fails, so the run
// form can show what the user is about to send.
func PreviewSnippetCommand(command string, values map[string]string) string {
	defs := ParseSnippetVariables(command)
	if len(defs) == 0 {
		return command
	}
	byName := make(map[string]SnippetVariableDef, len(defs))
	for _, def := range defs {
		byName[def.Name] = def
	}
	return replaceSnippetVariableTokens(command, func(name string) string {
		def, ok := byName[name]
		if !ok {
			return "{{" + name + "}}"
		}
		if value, resolved := resolveSnippetVariableValue(def, values); resolved {
			return value
		}
		return "{{" + def.Name + "}}"
	})
}

// resolveSnippetVariableValue applies the original precedence: a non-blank
// user value wins, then the declared default.
func resolveSnippetVariableValue(def SnippetVariableDef, values map[string]string) (string, bool) {
	if raw, ok := values[def.Name]; ok && strings.TrimSpace(raw) != "" {
		return raw, true
	}
	if def.HasDefault {
		return def.DefaultValue, true
	}
	return "", false
}

// replaceSnippetVariableTokens rewrites every non-Go-template token through
// replacementFor.
func replaceSnippetVariableTokens(command string, replacementFor func(name string) string) string {
	matches, goTemplate := collectSnippetVariableMatches(command)
	if len(matches) == 0 {
		return command
	}
	var builder strings.Builder
	builder.Grow(len(command))
	last := 0
	for _, match := range matches {
		name := strings.TrimSpace(match.Def.Name)
		if goTemplate[match.Start] || name == "" {
			continue
		}
		builder.WriteString(command[last:match.Start])
		builder.WriteString(replacementFor(name))
		last = match.End
	}
	builder.WriteString(command[last:])
	return builder.String()
}

// collectSnippetVariableMatches finds every token and marks the ones that are
// really Go template actions. The template scan mirrors the original: a
// {{range}}/{{if}}/{{with}} block opens at a field or assigned-variable
// reference, and control actions inside the block are template syntax rather
// than Snippet variables.
func collectSnippetVariableMatches(command string) ([]SnippetVariableMatch, map[int]bool) {
	indexes := snippetVariablePattern.FindAllStringSubmatchIndex(command, -1)
	matches := make([]SnippetVariableMatch, 0, len(indexes))
	for _, index := range indexes {
		match := SnippetVariableMatch{Start: index[0], End: index[1]}
		match.Def.Name = command[index[2]:index[3]]
		if index[4] >= 0 {
			match.Def.DefaultValue = command[index[4]:index[5]]
			match.Def.HasDefault = true
		}
		matches = append(matches, match)
	}

	goTemplate := make(map[int]bool)
	assigned := make(map[string]bool)
	depth := 0
	for _, match := range matches {
		action := normalizeGoTemplateAction(command[match.Start+2 : match.End-2])
		firstWord := firstGoTemplateWord(action)
		fieldReference := hasGoTemplateFieldReference(action)
		variableReference := hasGoTemplateVariableReference(action, assigned)
		insideBlock := depth > 0
		blockStart := (firstWord == "if" || firstWord == "range" || firstWord == "with") &&
			(fieldReference || variableReference)
		blockEnd := action == "end"
		token := fieldReference || variableReference || blockStart ||
			(insideBlock && isGoTemplateControlAction(action))

		if token {
			goTemplate[match.Start] = true
			for _, name := range goTemplateAssignedVariables(action) {
				assigned[name] = true
			}
		}
		switch {
		case blockStart:
			depth++
		case blockEnd && insideBlock:
			depth--
		}
	}
	return matches, goTemplate
}

// goTemplateAssignedVariables finds "$name" declarations such as the "$k" and
// "$v" in "range $k, $v := .Networks" or the "$p" in "$p := .LogPath".
func goTemplateAssignedVariables(action string) []string {
	var names []string
	for index := 0; index < len(action); index++ {
		if action[index] != '$' {
			continue
		}
		end := index + 1
		for end < len(action) && isGoTemplateIdentByte(action[end], end == index+1) {
			end++
		}
		if end == index+1 {
			continue
		}
		// The original lookahead accepts ",", "=", or ":=" after optional
		// whitespace.
		rest := strings.TrimLeft(action[end:], " \t")
		if strings.HasPrefix(rest, ":=") || strings.HasPrefix(rest, "=") ||
			strings.HasPrefix(rest, ",") {
			names = append(names, action[index:end])
		}
		index = end - 1
	}
	return names
}

func isGoTemplateIdentByte(b byte, first bool) bool {
	if b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') {
		return true
	}
	return !first && b >= '0' && b <= '9'
}

func normalizeGoTemplateAction(action string) string {
	action = strings.TrimSpace(action)
	action = strings.TrimPrefix(action, "-")
	action = strings.TrimSuffix(action, "-")
	return strings.TrimSpace(action)
}

func firstGoTemplateWord(action string) string {
	fields := strings.Fields(action)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// goTemplateFieldReferencePatterns are the field references the original
// treats as template syntax: a bare ".", ".Field", a trailing ".", or
// "$var.Field".
var goTemplateFieldReferencePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|[\s(])\.[A-Za-z_]`),
	regexp.MustCompile(`(?:^|[\s(])\.(?:$|[\s),|])`),
	regexp.MustCompile(`(?:^|[\s(])\$[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_]`),
}

func hasGoTemplateFieldReference(action string) bool {
	if action == "." {
		return true
	}
	for _, pattern := range goTemplateFieldReferencePatterns {
		if pattern.MatchString(action) {
			return true
		}
	}
	return false
}

func hasGoTemplateVariableReference(action string, variables map[string]bool) bool {
	for variable := range variables {
		pattern := regexp.MustCompile(
			`(?:^|[^A-Za-z0-9_])` + regexp.QuoteMeta(variable) + `(?:$|[^A-Za-z0-9_])`)
		if pattern.MatchString(action) {
			return true
		}
	}
	return false
}

func isGoTemplateControlAction(action string) bool {
	switch action {
	case "break", "continue", "else", "end":
		return true
	}
	firstWord := firstGoTemplateWord(action)
	switch firstWord {
	case "break", "continue", "else", "end", "if", "range", "with":
		return strings.HasPrefix(action, firstWord+" ")
	default:
		return false
	}
}
