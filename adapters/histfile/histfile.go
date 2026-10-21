// Package histfile reads the local shell's history files and exposes them as an
// autocomplete source. It mirrors the original's zsh, bash, and fish import:
// zsh EXTENDED_HISTORY records, bash HISTTIMEFORMAT records, and fish's YAML
// subset. Reading is best effort; a missing or unreadable file contributes
// nothing.
package histfile

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mslxl/natcatty/application/autocomplete"
)

const (
	// maxEntries caps the imported rows so a large history file does not make
	// the popup expensive.
	maxEntries = 500
	// maxReadBytes caps each file read. A larger file is read from its tail,
	// where the newest commands are.
	maxReadBytes = 512 * 1024
	// maxCommandLength drops pathological single-line records.
	maxCommandLength = 4096
	// aiHistoryMarker identifies commands the original's AI bridge executed.
	aiHistoryMarker = "__NCMCP_"
)

// Kind is the history file syntax.
type Kind int

const (
	// Zsh is a zsh history file, extended or plain.
	Zsh Kind = iota
	// Bash is a bash history file, with or without timestamps.
	Bash
	// Fish is a fish history file.
	Fish
)

// File is one candidate history file.
type File struct {
	Path string
	Kind Kind
}

// Entry is one parsed command. Timestamp is Unix seconds, zero when the file
// does not store one.
type Entry struct {
	Command   string
	Timestamp int64
}

// Options configures a Source.
type Options struct {
	// Name overrides the source name shown in suggestions. The default is
	// "shell-history".
	Name string
	// Files are read in order when the first suggestion is requested.
	Files []File
	// ReadFile replaces os.ReadFile, primarily for tests.
	ReadFile func(string) ([]byte, error)
	// MaxEntries overrides the import cap, primarily for tests.
	MaxEntries int
}

// Source implements autocomplete.Source over shell history files. The files
// are read once, lazily, so constructing a Session does not touch the disk.
type Source struct {
	name       string
	files      []File
	readFile   func(string) ([]byte, error)
	maxEntries int

	once    sync.Once
	entries []autocomplete.Suggestion
	// history keeps the same deduplicated, newest-first rows with their
	// timestamps, so the app can persist the import alongside Session
	// commands instead of leaving it suggestion-only.
	history []Entry
}

// New builds a Source from options.
func New(options Options) *Source {
	name := options.Name
	if name == "" {
		name = autocomplete.SourceShellHistory
	}
	readFile := options.ReadFile
	if readFile == nil {
		readFile = os.ReadFile
	}
	limit := options.MaxEntries
	if limit <= 0 || limit > maxEntries {
		limit = maxEntries
	}
	return &Source{
		name:       name,
		files:      append([]File(nil), options.Files...),
		readFile:   readFile,
		maxEntries: limit,
	}
}

// Default builds a Source over the shell history locations the original
// imports under home. The fish paths are both checked; the first that exists
// contributes its commands.
func Default(home string) *Source {
	if home == "" {
		return New(Options{})
	}
	return New(Options{Files: []File{
		{Path: filepath.Join(home, ".zsh_history"), Kind: Zsh},
		{Path: filepath.Join(home, ".bash_history"), Kind: Bash},
		{Path: filepath.Join(home, ".local", "share", "fish", "fish_history"), Kind: Fish},
		{Path: filepath.Join(home, ".config", "fish", "fish_history"), Kind: Fish},
	}})
}

// Name implements autocomplete.Source.
func (s *Source) Name() string { return s.name }

// Suggestions returns the imported commands that begin with input, newest
// first.
func (s *Source) Suggestions(input string, limit int) []autocomplete.Suggestion {
	input = strings.TrimSpace(input)
	if input == "" || limit <= 0 {
		return nil
	}
	s.once.Do(s.load)
	lowered := strings.ToLower(input)
	result := make([]autocomplete.Suggestion, 0, limit)
	for _, entry := range s.entries {
		if !strings.HasPrefix(strings.ToLower(entry.Text), lowered) {
			continue
		}
		result = append(result, entry)
		if len(result) >= limit {
			break
		}
	}
	return result
}

// History returns the imported, deduplicated commands newest first. The app
// persists them once at startup so the History surface lists shell commands
// as well as Session commands, subject to the same retention.
func (s *Source) History() []Entry {
	s.once.Do(s.load)
	return append([]Entry(nil), s.history...)
}

func (s *Source) paths() []string {
	paths := make([]string, 0, len(s.files))
	for _, file := range s.files {
		paths = append(paths, file.Path)
	}
	return paths
}

type sequencedEntry struct {
	Entry
	sequence int
}

func (s *Source) load() {
	merged := make([]sequencedEntry, 0, s.maxEntries)
	sequence := 0
	for _, file := range s.files {
		data, err := s.readFile(file.Path)
		if err != nil || len(data) == 0 {
			continue
		}
		text := decodeHistfile(data)
		var entries []Entry
		switch file.Kind {
		case Bash:
			entries = ParseBashHistory(text)
		case Fish:
			entries = ParseFishHistory(text)
		default:
			entries = ParseZshHistory(text)
		}
		for _, entry := range entries {
			merged = append(merged, sequencedEntry{Entry: entry, sequence: sequence})
			sequence++
		}
	}

	sort.SliceStable(merged, func(i, j int) bool {
		left, right := merged[i], merged[j]
		switch {
		case left.Timestamp > 0 && right.Timestamp > 0:
			return left.Timestamp > right.Timestamp
		case left.Timestamp > 0:
			return true
		case right.Timestamp > 0:
			return false
		default:
			// Neither has a timestamp: later in the file is newer.
			return left.sequence > right.sequence
		}
	})

	seen := make(map[string]struct{}, len(merged))
	entries := make([]autocomplete.Suggestion, 0, s.maxEntries)
	history := make([]Entry, 0, s.maxEntries)
	for _, entry := range merged {
		if entry.Command == "" || strings.Contains(entry.Command, aiHistoryMarker) {
			continue
		}
		if _, ok := seen[entry.Command]; ok {
			continue
		}
		seen[entry.Command] = struct{}{}
		score := 900 - len(entries)
		if score < 0 {
			score = 0
		}
		entries = append(entries, autocomplete.Suggestion{
			Text:    entry.Command,
			Display: entry.Command,
			Source:  s.name,
			Score:   score,
		})
		history = append(history, entry.Entry)
		if len(entries) >= s.maxEntries {
			break
		}
	}
	s.entries = entries
	s.history = history
}

// decodeHistfile converts a tail-possibly-truncated file read. A read that
// fills the budget may begin mid-record, so its first partial line is dropped.
func decodeHistfile(data []byte) string {
	if len(data) >= maxReadBytes {
		if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	return string(data)
}

var (
	zshExtendedRecord = regexp.MustCompile(`(?s)^: (\d+):\d+;(.*)$`)
	bashTimestamp     = regexp.MustCompile(`^#(\d{9,})$`)
	fishCommandLine   = regexp.MustCompile(`^- cmd:\s?(.*)$`)
	fishWhenLine      = regexp.MustCompile(`^\s+when:\s*(\d+)`)
)

func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// ParseZshHistory parses plain and EXTENDED_HISTORY zsh files. A line ending
// in an odd number of backslashes continues on the next physical line; the
// escaping backslash is removed.
func ParseZshHistory(text string) []Entry {
	if text == "" {
		return nil
	}
	var entries []Entry
	for _, record := range joinContinuations(splitLines(text)) {
		if match := zshExtendedRecord.FindStringSubmatch(record); match != nil {
			command := strings.TrimSpace(match[2])
			if command == "" || len(command) > maxCommandLength {
				continue
			}
			timestamp, _ := strconv.ParseInt(match[1], 10, 64)
			entries = append(entries, Entry{Command: command, Timestamp: timestamp})
			continue
		}
		command := strings.TrimSpace(record)
		if command == "" || len(command) > maxCommandLength {
			continue
		}
		entries = append(entries, Entry{Command: command})
	}
	return entries
}

func joinContinuations(lines []string) []string {
	records := make([]string, 0, len(lines))
	buffer := ""
	open := false
	for _, line := range lines {
		backslashes := 0
		for index := len(line) - 1; index >= 0 && line[index] == '\\'; index-- {
			backslashes++
		}
		continues := backslashes%2 == 1
		body := line
		if continues {
			body = line[:len(line)-1]
		}
		if open {
			buffer += "\n" + body
		} else {
			buffer = body
			open = true
		}
		if !continues {
			records = append(records, buffer)
			open = false
		}
	}
	if open {
		records = append(records, buffer)
	}
	return records
}

// ParseBashHistory parses bash history files. A #<epoch> line delimits a
// record, which is how bash stores multi-line commands under lithist.
func ParseBashHistory(text string) []Entry {
	if text == "" {
		return nil
	}
	var entries []Entry
	var pending []string
	var pendingTimestamp int64
	inTimestamped := false

	flush := func() {
		command := strings.TrimSpace(strings.Join(pending, "\n"))
		if command != "" && len(command) <= maxCommandLength {
			entries = append(entries, Entry{Command: command, Timestamp: pendingTimestamp})
		}
		pending = nil
		pendingTimestamp = 0
	}

	for _, line := range splitLines(text) {
		if match := bashTimestamp.FindStringSubmatch(line); match != nil {
			flush()
			pendingTimestamp, _ = strconv.ParseInt(match[1], 10, 64)
			inTimestamped = true
			continue
		}
		if inTimestamped {
			pending = append(pending, line)
			continue
		}
		command := strings.TrimSpace(line)
		if command == "" || len(command) > maxCommandLength {
			continue
		}
		entries = append(entries, Entry{Command: command})
	}
	if inTimestamped {
		flush()
	}
	return entries
}

// ParseFishHistory parses fish's `- cmd:` records and reverses its escaping of
// newlines and backslashes.
func ParseFishHistory(text string) []Entry {
	if text == "" {
		return nil
	}
	var entries []Entry
	open := false
	var current Entry
	for _, line := range splitLines(text) {
		if match := fishCommandLine.FindStringSubmatch(line); match != nil {
			if open && current.Command != "" {
				entries = append(entries, current)
			}
			open = false
			command := strings.TrimSpace(unescapeFish(match[1]))
			if command == "" || len(command) > maxCommandLength {
				current = Entry{}
				continue
			}
			current = Entry{Command: command}
			open = true
			continue
		}
		if !open {
			continue
		}
		if match := fishWhenLine.FindStringSubmatch(line); match != nil {
			current.Timestamp, _ = strconv.ParseInt(match[1], 10, 64)
		}
	}
	if open && current.Command != "" {
		entries = append(entries, current)
	}
	return entries
}

func unescapeFish(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '\\' && index+1 < len(value) {
			switch value[index+1] {
			case 'n':
				builder.WriteByte('\n')
				index++
				continue
			case '\\':
				builder.WriteByte('\\')
				index++
				continue
			}
		}
		builder.WriteByte(character)
	}
	return builder.String()
}
