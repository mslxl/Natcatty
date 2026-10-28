package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/internal/parity"
)

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9]+\.[a-z0-9-]+$`)
	ticketPattern = regexp.MustCompile(`^[0-9]{2}$`)
	qaPattern     = regexp.MustCompile(`^QA-[0-9]{3}$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

	statusOrder = []string{"verified", "implemented", "unverified", "excluded"}
	statuses    = map[string]bool{"unverified": true, "implemented": true, "verified": true, "excluded": true}
	exceptions  = map[string]bool{
		"ime": true, "screen-reader": true, "os-dialog": true, "installer": true,
		"physical-device": true, "multi-monitor": true, "renderer-only-visual": true,
	}
	allowedAreas = map[string]bool{}
	knownModules = map[string]bool{}
)

func init() {
	for _, area := range []string{
		"application", "connections", "terminal", "sessions", "sftp-transfer",
		"vault", "forwarding", "monitoring", "sysmanager", "browser", "settings",
		"search", "import-export", "desktop", "app-lock", "sync", "mcp", "i18n",
		"cross-cutting", "shell", "platform", "packaging",
	} {
		allowedAreas[area] = true
	}
	// The packages named by rewrite spec §2 and the §17 area roll-up. A module
	// may be listed before its package exists; internal/archtest enforces the
	// actual dependency direction once it does.
	for _, module := range []string{
		"domain/model", "domain/vault", "domain/connection", "domain/session", "domain/browser",
		"domain/workspace", "domain/transfer", "domain/forwarding", "domain/settings",
		"domain/sync", "domain/search", "domain/applock", "domain/history",
		"domain/keybindings", "domain/preferences", "domain/model/theme",
		"domain/model/serial", "domain/model/telnet", "domain/monitor",
		"application/model", "application/vault", "application/connection",
		"application/session", "application/workspace", "application/transfer",
		"application/forwarding", "application/settings", "application/sync",
		"application/search", "application/applock", "application/security",
		"application/command", "application/monitor", "application/sysmanager",
		"application/browser", "application/portability", "application/mcp",
		"application/desktop", "application/history",
		"application/keybindings", "application/preferences", "application/themes",
		"adapters/store", "adapters/ssh", "adapters/sftp", "adapters/scp", "adapters/gdrive",
		"adapters/onedrive", "adapters/webdav", "adapters/s3", "adapters/s3/fake",
		"adapters/serial", "adapters/telnet", "adapters/mosh", "adapters/et",
		"adapters/bundle",
		"adapters/secrets", "adapters/sync", "adapters/applock", "adapters/localpty",
		"adapters/mcprpc", "adapters/histfile",
		"ui/shell", "ui/shell/state", "ui/vault", "ui/vault/state", "ui/settings",
		"ui/applock", "ui/session", "ui/terminal", "ui/workspace", "ui/browser",
		"ui/transfer", "ui/forwarding", "ui/monitor", "ui/monitoring", "ui/sysmanager", "ui/search",
		"ui/desktop", "ui/tray", "ui/locales", "ui/logs", "ui/theme", "ui/serial",
		"platform/pty", "platform/secrets", "platform/sqlite", "platform/paths",
		"platform/xauth", "platform/hid",
		"app",
		"internal/mcp", "internal/mcp/catalog", "internal/adapters/mcprpc",
		"internal/tools/paritygen", "internal/archtest", "internal/testutil",
		"internal/testkit/fakessh",
		"cmd/natcatty", "cmd/natcatty-mcp", "cmd/launchsmoke",
	} {
		knownModules[module] = true
	}
}

type repo struct {
	root   string
	commit string
}

func originalPathSet(r repo) (map[string]bool, error) {
	if !commitPattern.MatchString(r.commit) {
		return nil, fmt.Errorf("invalid original commit %q", r.commit)
	}
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", r.commit)
	cmd.Dir = r.root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list original paths at %s: %v: %s", r.commit, err, strings.TrimSpace(stderr.String()))
	}
	paths := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths[line] = true
		}
	}
	return paths, nil
}

// isCleanRelPath reports whether value is a repository-relative path with no
// traversal and no platform-specific separators.
func isCleanRelPath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	return path.Clean(value) == value && !strings.HasPrefix(value, "../")
}

func validate(r repo, m *parity.Matrix, tests parity.TestIndex) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	limitationData, err := os.ReadFile(filepath.Join(r.root, parity.LimitationsPath))
	if err != nil {
		add("read %s: %v", parity.LimitationsPath, err)
	}
	limitations := parity.ParseLimitations(limitationData)
	limitationByID := map[string]parity.Limitation{}
	for _, entry := range limitations {
		if _, ok := limitationByID[entry.ID]; ok {
			add("limitations.md defines %q more than once", entry.ID)
			continue
		}
		limitationByID[entry.ID] = entry
	}

	originalPaths, gitErr := originalPathSet(r)
	if gitErr != nil {
		add("%v (fetch full history, e.g. actions/checkout fetch-depth: 0)", gitErr)
	}

	seenIDs := map[string]bool{}
	for index := range m.Rows {
		row := &m.Rows[index]
		where := func(format string, args ...any) {
			prefix := fmt.Sprintf("row %q (line %d): ", row.ID, row.Line)
			errs = append(errs, fmt.Errorf(prefix+format, args...))
		}

		if !idPattern.MatchString(row.ID) {
			where("invalid id: must match %s", idPattern)
		}
		if seenIDs[row.ID] {
			where("duplicate id")
		}
		seenIDs[row.ID] = true

		if !allowedAreas[row.Area] {
			where("unknown area %q", row.Area)
		}
		if strings.TrimSpace(row.Feature) == "" {
			where("feature is empty")
		}

		if len(row.OriginalPaths) == 0 {
			where("original_paths is empty")
		}
		for _, originalPath := range row.OriginalPaths {
			if !isCleanRelPath(originalPath) {
				where("original path %q must be a clean forward-slash path relative to the repository root", originalPath)
				continue
			}
			if gitErr == nil && !originalPaths[originalPath] {
				where("original path %q does not exist at commit %s", originalPath, r.commit)
			}
		}

		if row.Module == "" {
			where("module is empty")
		} else if !isCleanRelPath(row.Module) || !knownModules[row.Module] {
			where("unknown module %q: expected a package from rewrite spec §2", row.Module)
		}

		if len(row.Tickets) == 0 {
			where("tickets is empty")
		}
		// The native-build ticket set is fixed at 01-60 by the rewrite spec.
		// .scratch/ is gitignored, so CI cannot read the ticket files; checking
		// the number against that fixed set keeps the gate deterministic.
		for _, ticket := range row.Tickets {
			if !ticketPattern.MatchString(ticket) || !validTicketNumber(ticket) {
				where("unknown ticket %q: expected 01-60", ticket)
			}
		}

		if !statuses[row.Status] {
			where("invalid status %q: expected unverified, implemented, verified, or excluded", row.Status)
		}

		if row.AutomationException != "" {
			if !exceptions[row.AutomationException] {
				where("unknown automation_exception %q", row.AutomationException)
			}
			if len(row.ManualQA) == 0 {
				where("automation_exception %q needs at least one manual_qa item", row.AutomationException)
			}
		}
		for _, qa := range row.ManualQA {
			if !qaPattern.MatchString(qa) {
				where("manual_qa item %q must look like QA-001", qa)
			}
		}

		for _, entry := range row.AcceptanceTests {
			if strings.HasPrefix(entry, "QA-") {
				if !qaPattern.MatchString(entry) {
					where("QA item %q must look like QA-001", entry)
				}
				if !contains(row.ManualQA, entry) {
					where("QA item %q must also appear in manual_qa", entry)
				}
				if row.AutomationException == "" {
					where("QA item %q needs an automation_exception", entry)
				}
				continue
			}
			if !tests.Resolves(entry) {
				where("acceptance test %q does not resolve to a Go test", entry)
			}
		}
		if len(row.AcceptanceTests) == 0 && row.Status != "excluded" {
			if len(row.ManualQA) == 0 || row.AutomationException == "" {
				where("needs an acceptance test, or manual_qa plus automation_exception")
			}
		}
		// A verified row is a claim that the behavior is covered by the
		// default test jobs. A test behind the selftest or launchsmoke tag is
		// real evidence, but `go test ./...` does not run it, so it cannot be
		// the only evidence for verified. A row may keep such a test in its
		// list as long as another named test runs in the default build.
		if row.Status == "verified" && !parity.HasCIRunnableTest(row, tests) {
			where("status verified needs at least one acceptance test that runs in the default CI build")
		}

		if row.Status == "excluded" {
			limitation, ok := limitationByID[row.ID]
			if !ok {
				where("excluded row needs an entry in %s", parity.LimitationsPath)
			} else {
				if !hasLinePrefix(limitation.Body, "Decision:") {
					where("excluded row needs a \"Decision:\" line in %s", parity.LimitationsPath)
				}
				if !hasLinePrefix(limitation.Body, "Ticket:") {
					where("excluded row needs a \"Ticket:\" sign-off line in %s", parity.LimitationsPath)
				}
			}
			if strings.TrimSpace(row.Notes) == "" {
				where("excluded row needs notes explaining the gap")
			}
		}
	}

	for _, limitation := range limitations {
		row := parity.FindByID(m.Rows, limitation.ID)
		if row == nil {
			add("limitations.md has an entry for %q, but no matrix row uses that id", limitation.ID)
			continue
		}
		if row.Status != "excluded" {
			add("limitations.md has an entry for %q, but the row is not excluded", limitation.ID)
		}
	}
	return errs
}

// validateRelease applies the release-readiness rules from rewrite spec §17
// rule 7 and the ticket-coverage half of rule 4. Every non-excluded row must
// be verified, every exclusion must be signed in limitations.md, every
// verified row must name a test the default CI jobs run, and every ticket
// 01-60 must appear on a row. CI runs it at release time; the PR gate keeps
// unverified work visible instead.
func validateRelease(r repo, m *parity.Matrix, tests parity.TestIndex) []error {
	var errs []error
	limitationData, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(parity.LimitationsPath)))
	if err != nil {
		errs = append(errs, fmt.Errorf("read %s: %v", parity.LimitationsPath, err))
	}
	for _, problem := range parity.Audit(m, parity.ParseLimitations(limitationData)) {
		errs = append(errs, fmt.Errorf("%s", problem))
	}
	for index := range m.Rows {
		row := &m.Rows[index]
		if row.Status != "verified" {
			continue
		}
		if !parity.HasCIRunnableTest(row, tests) {
			errs = append(errs, fmt.Errorf(
				"row %q names no acceptance test that runs in the default CI build", row.ID))
		}
	}
	covered := map[string]bool{}
	for _, row := range m.Rows {
		for _, ticket := range row.Tickets {
			covered[ticket] = true
		}
	}
	for number := 1; number <= 60; number++ {
		ticket := fmt.Sprintf("%02d", number)
		if !covered[ticket] {
			errs = append(errs, fmt.Errorf("ticket %s has no parity row", ticket))
		}
	}
	return errs
}

func hasLinePrefix(body, prefix string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

func validTicketNumber(ticket string) bool {
	number, err := strconv.Atoi(ticket)
	return err == nil && number >= 1 && number <= 60
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
