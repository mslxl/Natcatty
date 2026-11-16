package processcwd

import "testing"

// TestParseLsofOutput covers the macOS probe's records: a directory, a
// directory whose type arrives before its name, and names lsof annotated as
// unresolved.
func TestParseLsofOutput(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "directory", output: "p123\nfcwd\ntDIR\nn/Users/me/project\n", want: "/Users/me/project"},
		{name: "name before type", output: "p123\nfcwd\nn/Users/me/project\ntDIR\n", want: "/Users/me/project"},
		{name: "not a directory", output: "p123\nfcwd\ntREG\nn/Users/me/file\n"},
		{name: "permission note", output: "fcwd\nn/Users/me/project (readlink: Permission denied)\n"},
		{name: "relative", output: "fcwd\nnrelative/path\n"},
		{name: "empty", output: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseLsofOutput(test.output); got != test.want {
				t.Fatalf("parseLsofOutput = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInvalidProcessID(t *testing.T) {
	if _, err := Of(0); err == nil {
		t.Fatal("Of(0) succeeded, want an invalid process error")
	}
}
