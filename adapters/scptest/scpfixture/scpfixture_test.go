package scpfixture

import "testing"

// TestLsModeRendersEveryPermissionBit covers the bit positions the upload
// transaction verifies: group and other write (a Windows source reports 0666)
// and the special execute markers. A fixture that only clears bits from a
// fixed literal would report 0666 as 0644 and fail a correct upload.
func TestLsModeRendersEveryPermissionBit(t *testing.T) {
	cases := []struct {
		name string
		node Node
		want string
	}{
		{"file 0644", Node{Type: File, Mode: 0o644}, "-rw-r--r--"},
		{"file 0666", Node{Type: File, Mode: 0o666}, "-rw-rw-rw-"},
		{"file 0755", Node{Type: File, Mode: 0o755}, "-rwxr-xr-x"},
		{"setuid executable", Node{Type: File, Mode: 0o4755}, "-rwsr-xr-x"},
		{"setgid without execute", Node{Type: File, Mode: 0o2644}, "-rw-r-Sr--"},
		{"sticky directory", Node{Type: Directory, Mode: 0o1777}, "drwxrwxrwt"},
		{"symlink", Node{Type: Symlink, Mode: 0o777}, "lrwxrwxrwx"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := lsMode(&testCase.node); got != testCase.want {
				t.Fatalf("lsMode(%04o) = %q, want %q", testCase.node.Mode, got, testCase.want)
			}
		})
	}
}
