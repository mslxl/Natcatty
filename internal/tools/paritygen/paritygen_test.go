package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a throwaway module with a git history. The matrix linter reads
// original paths from a commit, so tests use a real repository rather than a
// mocked PATH.
type fixture struct {
	root   string
	commit string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/fixture\n\ngo 1.27.1\n")
	writeFile(t, root, "original/src.ts", "export const original = true;\n")
	writeFile(t, root, "ui/locales/locales.go", "package foo\n")
	writeFile(t, root, "ui/locales/i18n_test.go", "package foo\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {}\n")
	writeFile(t, root, "docs/parity/limitations.md", "# Parity limitations\n")

	runGit(t, root, "init", "-q")
	runGit(t, root, "add", "-A")
	runGit(t, root, "-c", "user.name=Parity Test", "-c", "user.email=parity@example.com",
		"commit", "-q", "-m", "fixture")
	commit := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))

	writeFile(t, root, "docs/parity/matrix.yaml", validMatrix())
	return &fixture{root: root, commit: commit}
}

func validMatrix() string {
	return `# Seeded parity matrix.
- id: ui.foo
  area: shell
  feature: Foo capability
  original_paths:
    - original/src.ts
  module: ui/locales
  tickets: [01]
  acceptance_tests:
    - ui/locales.TestFoo
  status: verified
  notes: ""
`
}

func writeMatrix(t *testing.T, f *fixture, contents string) {
	t.Helper()
	writeFile(t, f.root, "docs/parity/matrix.yaml", contents)
}

func runParitygen(t *testing.T, f *fixture, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	args = append(args, "-commit", f.commit)
	var out, errOut bytes.Buffer
	code = run(args, f.root, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRunGeneratesThenVerifiesTheMatrix(t *testing.T) {
	f := newFixture(t)

	code, stdout, stderr := runParitygen(t, f)
	if code != 0 {
		t.Fatalf("run() = %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "1 rows") {
		t.Errorf("stdout = %q, want a row count", stdout)
	}

	generated, err := os.ReadFile(filepath.Join(f.root, "docs/parity/matrix.md"))
	if err != nil {
		t.Fatalf("read generated matrix: %v", err)
	}
	for _, want := range []string{"# Parity Matrix", "## shell", "ui.foo", "original/src.ts", "ui/locales.TestFoo"} {
		if !strings.Contains(string(generated), want) {
			t.Errorf("matrix.md missing %q:\n%s", want, generated)
		}
	}

	code, _, stderr = runParitygen(t, f, "-check")
	if code != 0 {
		t.Fatalf("check after generate = %d, stderr:\n%s", code, stderr)
	}
}

func TestCheckRejectsAStaleGeneratedMatrix(t *testing.T) {
	f := newFixture(t)
	if code, _, stderr := runParitygen(t, f); code != 0 {
		t.Fatalf("generate = %d, stderr:\n%s", code, stderr)
	}

	writeMatrix(t, f, strings.Replace(validMatrix(), "Foo capability", "Changed capability", 1))
	code, _, stderr := runParitygen(t, f, "-check")
	if code == 0 {
		t.Fatal("check accepted a stale matrix.md")
	}
	if !strings.Contains(stderr, "stale") {
		t.Errorf("stderr = %q, want a stale-file message", stderr)
	}
}

func TestLintRejectsBrokenRows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{
			name: "duplicate id",
			mutate: func(m string) string {
				return m + strings.Replace(m, "  status: verified", "  status: unverified", 1)
			},
			want: "duplicate id",
		},
		{
			name: "missing original path",
			mutate: func(m string) string {
				return strings.Replace(m, "original/src.ts", "original/missing.ts", 1)
			},
			want: "does not exist at commit",
		},
		{
			name: "unknown module",
			mutate: func(m string) string {
				return strings.Replace(m, "module: ui/locales", "module: nowhere/foo", 1)
			},
			want: "unknown module",
		},
		{
			name: "unknown ticket",
			mutate: func(m string) string {
				return strings.Replace(m, "tickets: [01]", "tickets: [99]", 1)
			},
			want: "unknown ticket",
		},
		{
			name: "unresolvable acceptance test",
			mutate: func(m string) string {
				return strings.Replace(m, "ui/locales.TestFoo", "ui/foo.TestMissing", 1)
			},
			want: "does not resolve to a Go test",
		},
		{
			name: "invalid status",
			mutate: func(m string) string {
				return strings.Replace(m, "status: verified", "status: done", 1)
			},
			want: "invalid status",
		},
		{
			name: "invalid id",
			mutate: func(m string) string {
				return strings.Replace(m, "id: ui.foo", "id: UI_Foo", 1)
			},
			want: "invalid id",
		},
		{
			name: "invalid area",
			mutate: func(m string) string {
				return strings.Replace(m, "area: shell", "area: made-up", 1)
			},
			want: "unknown area",
		},
		{
			name: "non-excluded row without an acceptance test",
			mutate: func(m string) string {
				return strings.Replace(m, "    - ui/locales.TestFoo\n", "", 1)
			},
			want: "needs an acceptance test",
		},
		{
			name: "unknown field",
			mutate: func(m string) string {
				return strings.Replace(m, "  notes: \"\"", "  owner: nobody", 1)
			},
			want: "unknown field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			writeMatrix(t, f, test.mutate(validMatrix()))

			code, _, stderr := runParitygen(t, f, "-check")
			if code == 0 {
				t.Fatalf("check accepted an invalid matrix")
			}
			if !strings.Contains(stderr, test.want) {
				t.Errorf("stderr = %q, want %q", stderr, test.want)
			}
		})
	}
}

func excludedMatrix() string {
	return `- id: ui.foo
  area: shell
  feature: Foo capability
  original_paths:
    - original/src.ts
  module: ui/locales
  tickets: [01]
  status: excluded
  notes: "Not available in the native toolkit."
`
}

func TestExcludedRowsNeedASignedLimitationEntry(t *testing.T) {
	f := newFixture(t)
	writeMatrix(t, f, excludedMatrix())

	code, _, stderr := runParitygen(t, f, "-check")
	if code == 0 {
		t.Fatal("check accepted an excluded row without a limitation")
	}
	if !strings.Contains(stderr, "limitations.md") {
		t.Errorf("stderr = %q, want a limitations.md message", stderr)
	}

	writeFile(t, f.root, "docs/parity/limitations.md", `# Parity limitations

### `+"`ui.foo`"+`
The native toolkit has no equivalent.

Decision: accepted for v1.
Ticket: 01
`)
	code, _, stderr = runParitygen(t, f)
	if code != 0 {
		t.Fatalf("run rejected a signed limitation = %d, stderr:\n%s", code, stderr)
	}
}

func TestLimitationEntriesMustHaveADecisionAndMatchAnExcludedRow(t *testing.T) {
	t.Run("missing decision", func(t *testing.T) {
		f := newFixture(t)
		writeMatrix(t, f, excludedMatrix())
		writeFile(t, f.root, "docs/parity/limitations.md", "# Parity limitations\n\n### `ui.foo`\nNo reason yet.\n")

		code, _, stderr := runParitygen(t, f, "-check")
		if code == 0 {
			t.Fatal("check accepted a limitation without a decision")
		}
		if !strings.Contains(stderr, "Decision:") {
			t.Errorf("stderr = %q, want a Decision requirement", stderr)
		}
	})

	t.Run("missing sign-off ticket", func(t *testing.T) {
		f := newFixture(t)
		writeMatrix(t, f, excludedMatrix())
		writeFile(t, f.root, "docs/parity/limitations.md", "# Parity limitations\n\n### `ui.foo`\nReason.\n\nDecision: accepted.\n")

		code, _, stderr := runParitygen(t, f, "-check")
		if code == 0 {
			t.Fatal("check accepted a limitation without a ticket")
		}
		if !strings.Contains(stderr, "Ticket:") {
			t.Errorf("stderr = %q, want a Ticket requirement", stderr)
		}
	})

	t.Run("orphan limitation", func(t *testing.T) {
		f := newFixture(t)
		writeFile(t, f.root, "docs/parity/limitations.md", "# Parity limitations\n\n### `ui.other`\nReason.\n\nDecision: accepted.\nTicket: 01\n")

		code, _, stderr := runParitygen(t, f, "-check")
		if code == 0 {
			t.Fatal("check accepted a limitation for a row that does not exist")
		}
		if !strings.Contains(stderr, "ui.other") {
			t.Errorf("stderr = %q, want the orphaned id", stderr)
		}
	})
}

func TestManualQARowsNeedAnAllowedAutomationException(t *testing.T) {
	manual := `- id: ui.foo
  area: shell
  feature: Foo capability
  original_paths:
    - original/src.ts
  module: ui/locales
  tickets: [01]
  manual_qa: [QA-001]
  automation_exception: ime
  status: implemented
  notes: "Candidate-window placement needs a human."
`
	f := newFixture(t)
	writeMatrix(t, f, manual)
	if code, _, stderr := runParitygen(t, f); code != 0 {
		t.Fatalf("run rejected an allowed manual exception = %d, stderr:\n%s", code, stderr)
	}

	writeFile(t, f.root, "docs/parity/matrix.yaml", strings.Replace(manual, "automation_exception: ime", "automation_exception: vibes", 1))
	code, _, stderr := runParitygen(t, f, "-check")
	if code == 0 {
		t.Fatal("check accepted an unknown automation exception")
	}
	if !strings.Contains(stderr, "automation_exception") {
		t.Errorf("stderr = %q, want an automation_exception message", stderr)
	}
}

func TestReleaseRequiresVerifiedRowsAndTicketCoverage(t *testing.T) {
	f := newFixture(t)
	writeMatrix(t, f, validMatrix())

	code, _, stderr := runParitygen(t, f, "-release")
	if code == 0 {
		t.Fatal("release accepted an incomplete matrix")
	}
	for _, want := range []string{"ticket 02 has no parity row", "ticket 60 has no parity row"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}

	writeMatrix(t, f, strings.Replace(validMatrix(), "status: verified", "status: implemented", 1))
	code, _, stderr = runParitygen(t, f, "-release")
	if code == 0 {
		t.Fatal("release accepted an implemented row")
	}
	if !strings.Contains(stderr, "release requires every non-excluded row to be verified") {
		t.Errorf("stderr = %q, want a verification requirement", stderr)
	}
}

func TestRenderSortsByAreaThenIDAndIsDeterministic(t *testing.T) {
	f := newFixture(t)
	matrix := `- id: ui.zeta
  area: vault
  feature: Zeta
  original_paths: [original/src.ts]
  module: ui/locales
  tickets: [01]
  acceptance_tests: [ui/locales.TestFoo]
  status: verified
  notes: ""
- id: ui.alpha
  area: shell
  feature: Alpha
  original_paths:
    - original/src.ts
  module: ui/locales
  tickets: [01]
  acceptance_tests:
    - ui/locales.TestFoo
  status: verified
  notes: ""
`
	writeMatrix(t, f, matrix)
	if code, _, stderr := runParitygen(t, f); code != 0 {
		t.Fatalf("generate = %d, stderr:\n%s", code, stderr)
	}
	first, err := os.ReadFile(filepath.Join(f.root, "docs/parity/matrix.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(first), "ui.alpha") > strings.Index(string(first), "ui.zeta") {
		t.Error("matrix.md is not sorted by area")
	}
	if code, _, stderr := runParitygen(t, f); code != 0 {
		t.Fatalf("regenerate = %d, stderr:\n%s", code, stderr)
	}
	second, err := os.ReadFile(filepath.Join(f.root, "docs/parity/matrix.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("regeneration is not deterministic")
	}
}

func writeFile(t *testing.T, root, rel, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
