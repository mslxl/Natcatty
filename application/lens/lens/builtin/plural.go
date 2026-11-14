package builtin

import "strconv"

// plural renders "<n> <one|many>" with the noun that matches the count: Terminal's
// views say "1 entry" / "2 entries", "1 file changed" / "2 files changed".
func plural(n int, one, many string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + one
	}
	return strconv.Itoa(n) + " " + many
}
