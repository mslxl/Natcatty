package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type runOptions struct {
	composeFile string
	outDir      string
	keep        bool
	verbose     bool
	timeout     time.Duration
	labAddr     string
	root        string
}

// suiteSpec is one `go test -json` invocation in a cell.
type suiteSpec struct {
	name     string
	packages []string
	env      []string
	skip     string
}

func runCell(ctx context.Context, cell Cell, opts runOptions) (CellReport, error) {
	report := CellReport{
		Schema:        reportSchema,
		Cell:          cell,
		GeneratedAt:   time.Now().UTC(),
		Commit:        gitCommit(opts.root),
		TreeHash:      gitTreeHash(opts.root),
		GoVersion:     runtime.Version(),
		HostOS:        runtime.GOOS,
		HostArch:      runtime.GOARCH,
		RunID:         strings.TrimSpace(os.Getenv("GITHUB_RUN_ID")),
		RunnerName:    strings.TrimSpace(os.Getenv("RUNNER_NAME")),
		ServerVersion: cell.Version,
	}
	// A report produced from uncommitted code cannot prove the checkout under
	// test, so refuse before doing any Docker work and without writing a
	// report. The output directory is excluded because it is where reports are
	// written (and is normally gitignored).
	if err := requireCleanWorktree(opts.root, opts.outDir); err != nil {
		return report, err
	}
	if runtime.GOOS != cell.GOOS || runtime.GOARCH != cell.GOARCH {
		// Local development on another host is allowed for behavior smoke
		// checks, but the report cannot pass verification: the matrix expects
		// the cell to have run on its CI runner platform. Say so loudly rather
		// than leaving an apparently green report the gate will reject.
		fmt.Fprintf(os.Stderr,
			"warning: %s is running on %s/%s but the matrix expects %s/%s; this report will be rejected by verify outside the expected runner\n",
			cell.ID, runtime.GOOS, runtime.GOARCH, cell.GOOS, cell.GOARCH)
	}
	labAddr := cell.SSHAddr
	if opts.labAddr != "" {
		if err := validateLabOverride(cell, opts.labAddr); err != nil {
			return finish(report, opts, err)
		}
		labAddr = opts.labAddr
	}
	if err := validateLoopbackAddress(labAddr); err != nil {
		return finish(report, opts, err)
	}
	report.LabAddress = labAddr

	if cell.Native {
		if err := waitTCP(ctx, labAddr, 60*time.Second); err != nil {
			return finish(report, opts, fmt.Errorf("native cell %s is not listening: %w", cell.ID, err))
		}
	} else {
		if err := startDockerCell(ctx, cell, opts); err != nil {
			saveContainerLogs(cell, opts, "start")
			if !opts.keep {
				stopDockerCell(context.Background(), cell, opts)
			}
			return finish(report, opts, err)
		}
		if !opts.keep {
			defer func() { stopDockerCell(context.Background(), cell, opts) }()
		}
		if err := waitTCP(ctx, labAddr, 180*time.Second); err != nil {
			saveContainerLogs(cell, opts, "startup")
			return finish(report, opts, fmt.Errorf("cell %s SSH endpoint %s: %w", cell.ID, labAddr, err))
		}
	}

	labEnv := suiteEnv(os.Environ(), CellEnv(cell, labAddr))
	suites := []suiteSpec{
		{
			name:     suiteLab,
			packages: []string{"./adapters/et"},
			env:      labEnv,
			skip:     skipPattern(cell.Version),
		},
		{
			name:     suiteUnit,
			packages: []string{"./adapters/et/..."},
			env:      suiteEnv(os.Environ(), nil),
		},
	}
	for _, suite := range suites {
		result, err := runSuite(ctx, opts, suite)
		report.Suites = append(report.Suites, result)
		if err != nil && ctx.Err() != nil {
			return finish(report, opts, ctx.Err())
		}
		if !opts.keep && !cell.Native {
			saveContainerLogs(cell, opts, suite.name)
		}
	}

	report.Behaviors = evaluateCell(cell, report.Suites)
	report.Status = "pass"
	for _, suite := range report.Suites {
		if suite.Status != "pass" {
			report.Status = "fail"
		}
	}
	for _, behavior := range report.Behaviors {
		if behavior.Status != "pass" {
			report.Status = "fail"
		}
	}
	if err := writeReport(opts.outDir, report); err != nil {
		return report, err
	}
	printCellResult(os.Stdout, report, opts.verbose)
	if report.Status != "pass" {
		return report, fmt.Errorf("cell %s failed", cell.ID)
	}
	return report, nil
}

func finish(report CellReport, opts runOptions, err error) (CellReport, error) {
	report.Status = "fail"
	if report.Behaviors == nil {
		report.Behaviors = evaluateCell(report.Cell, report.Suites)
	}
	if writeErr := writeReport(opts.outDir, report); writeErr != nil {
		return report, errors.Join(err, writeErr)
	}
	printCellResult(os.Stdout, report, opts.verbose)
	return report, err
}

func printCellResult(w io.Writer, report CellReport, verbose bool) {
	green, total := 0, 0
	for _, behavior := range report.Behaviors {
		total++
		if behavior.Status == "pass" {
			green++
		}
	}
	fmt.Fprintf(w, "%-24s %-10s %-5s %d/%d behaviors\n",
		report.Cell.ID, report.ServerVersion, strings.ToUpper(report.Status), green, total)
	if report.Status == "pass" && !verbose {
		return
	}
	for _, behavior := range report.Behaviors {
		if behavior.Status == "pass" && !verbose {
			continue
		}
		fmt.Fprintf(w, "  %-24s %s\n", behavior.ID, behavior.Status)
		for _, check := range behavior.Checks {
			if check.Status == "pass" && !verbose {
				continue
			}
			fmt.Fprintf(w, "    %-8s %s/%s: %s (%s)\n",
				check.Suite, check.Package, check.Name, check.Status, check.Detail)
		}
	}
	for _, suite := range report.Suites {
		if suite.Status == "pass" && !verbose {
			continue
		}
		fmt.Fprintf(w, "  suite %s: %s in %s\n", suite.Name, suite.Status, suite.Elapsed.Round(time.Second))
		if suite.Output != "" {
			fmt.Fprintln(w, indent(suite.Output, "    "))
		}
	}
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func runSuite(ctx context.Context, opts runOptions, spec suiteSpec) (SuiteResult, error) {
	result := SuiteResult{Name: spec.name, Packages: spec.packages}
	args := []string{"test", "-json", "-count=1", "-timeout", opts.timeout.String()}
	if spec.skip != "" {
		args = append(args, "-skip", spec.skip)
	}
	args = append(args, spec.packages...)
	goBin, err := goBinary()
	if err != nil {
		result.Status = "fail"
		result.Output = err.Error()
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout+2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = opts.root
	cmd.Env = spec.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	runErr := cmd.Run()
	result.Elapsed = time.Since(started)
	result.Status = "pass"
	if runErr != nil {
		result.Status = "fail"
	}
	tests, parseErr := parseGoTestJSON(bytes.NewReader(stdout.Bytes()))
	if parseErr != nil {
		result.Status = "fail"
		result.Output = "parsing go test JSON: " + parseErr.Error()
	}
	result.Tests = tests
	combined := stdout.String()
	if stderr.Len() > 0 {
		combined += "\n" + stderr.String()
	}
	result.Output = truncate(combined, 256<<10)
	return result, runErr
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "\n... truncated ..."
}

// truncateTail keeps the end of text. Build and test tools print the
// diagnosis last, so a failing command's error must keep the tail rather than
// the progress output that filled the head.
func truncateTail(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return "... truncated ...\n" + text[len(text)-limit:]
}

func goBinary() (string, error) {
	if path, err := exec.LookPath("go"); err == nil {
		return path, nil
	}
	candidate := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", errors.New("no go command on PATH and none in GOROOT")
}

func startDockerCell(ctx context.Context, cell Cell, opts runOptions) error {
	if cell.Service == "" {
		return fmt.Errorf("cell %s has no compose service", cell.ID)
	}
	if _, err := dockerBinary(); err != nil {
		return fmt.Errorf("cell %s needs Docker: %w", cell.ID, err)
	}
	upCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	if err := compose(upCtx, opts, "build", cell.Service); err != nil {
		return fmt.Errorf("build %s: %w", cell.Service, err)
	}
	if err := compose(upCtx, opts, "up", "-d", "--wait", cell.Service); err != nil {
		return fmt.Errorf("start %s: %w", cell.Service, err)
	}
	// Prove the running cell image was built from the base digest the matrix
	// pins. The unit test checks the checked-in compose file, but -compose can
	// point at another file and compose can interpolate BASE from the
	// environment; the image label records the BASE actually built.
	if err := verifyRuntimeBaseDigest(ctx, opts, cell); err != nil {
		return fmt.Errorf("verify %s runtime base image: %w", cell.ID, err)
	}
	// Prove the deterministic host port rather than assuming compose applied
	// it: a collision or an edited compose file must not silently test
	// another cell's endpoint.
	portText, err := composeOutput(ctx, opts, "port", cell.Service, "22")
	if err != nil {
		return fmt.Errorf("resolve published SSH port for %s: %w", cell.Service, err)
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(portText))
	if err != nil {
		return fmt.Errorf("compose port output %q: %w", portText, err)
	}
	_ = host
	found := "127.0.0.1:" + port
	if found != cell.SSHAddr {
		return fmt.Errorf("cell %s published SSH at %s, matrix pins %s", cell.ID, found, cell.SSHAddr)
	}
	return nil
}

// verifyRuntimeBaseDigest proves the container that will serve the cell was
// built from the base image digest pinned in the matrix. The compose unit test
// checks the checked-in file, but `run -compose` can point at another file and
// compose can interpolate BASE from the environment. The image label is set
// from the build argument actually used, so it catches both. This is a drift
// check, not cryptographic attestation: the artifact trust boundary is
// documented in adapters/et/lab/README.md.
func verifyRuntimeBaseDigest(ctx context.Context, opts runOptions, cell Cell) error {
	ids, err := composeOutput(ctx, opts, "ps", "-q", cell.Service)
	if err != nil {
		return err
	}
	var containers []string
	for _, line := range strings.Split(ids, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			containers = append(containers, line)
		}
	}
	if len(containers) != 1 {
		return fmt.Errorf("compose service %s has %d running containers, want 1", cell.Service, len(containers))
	}
	description, err := dockerOutput(ctx, opts.root, "inspect", "--format",
		`{{index .Config.Labels "org.opencontainers.image.description"}}`, containers[0])
	if err != nil {
		return err
	}
	return checkBaseImageDescription(description, cell.BaseImageDigest)
}

// checkBaseImageDescription requires the running image's description to name
// the exact base digest, matched as a whole `@sha256:...` reference.
func checkBaseImageDescription(description, digest string) error {
	if !imageDigestPattern.MatchString(digest) {
		return fmt.Errorf("matrix base image digest %q is not a full sha256 digest", digest)
	}
	if !strings.Contains(description, "@"+digest) {
		return fmt.Errorf("running image description %q does not name the pinned base digest %s",
			strings.TrimSpace(description), digest)
	}
	return nil
}

// dockerOutput runs a docker command in root and returns its combined output.
func dockerOutput(ctx context.Context, root string, args ...string) (string, error) {
	return runDocker(ctx, root, args...)
}

func stopDockerCell(ctx context.Context, cell Cell, opts runOptions) {
	if cell.Native || cell.Service == "" {
		return
	}
	downCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_ = compose(downCtx, opts, "down", "--remove-orphans", cell.Service)
}

func saveContainerLogs(cell Cell, opts runOptions, suite string) {
	if cell.Native || cell.Service == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	logs, err := composeOutput(ctx, opts, "logs", "--no-color", "--tail", "2000", cell.Service)
	if err != nil {
		return
	}
	_ = os.MkdirAll(opts.outDir, 0o755)
	path := filepath.Join(opts.outDir, sanitizeCellID(cell.ID)+"-"+suite+".log")
	_ = os.WriteFile(path, []byte(logs), 0o644)
}

func dockerBinary() (string, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return "", errors.New("docker is not installed")
	}
	return path, nil
}

func compose(ctx context.Context, opts runOptions, args ...string) error {
	_, err := composeOutput(ctx, opts, args...)
	return err
}

func composeOutput(ctx context.Context, opts runOptions, args ...string) (string, error) {
	full := append([]string{"compose", "-f", opts.composeFile}, args...)
	return runDocker(ctx, opts.root, full...)
}

// runDocker runs docker with args in root and returns its combined output.
func runDocker(ctx context.Context, root string, args ...string) (string, error) {
	docker, err := dockerBinary()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, docker, args...)
	cmd.Dir = root
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("docker %s: %w: %s",
			strings.Join(args, " "), err, truncateTail(output.String(), 8192))
	}
	return output.String(), nil
}

// waitTCP polls a real connection until it succeeds or the hard deadline
// passes. The poll cadence is not a synchronization assumption: each attempt
// checks the actual listening socket.
func waitTCP(ctx context.Context, address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("not accepting connections after %s: %w", timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// mergeEnv returns base with the given KEY=VALUE entries replacing, not
// duplicating, any existing key. An entry with an empty value removes the key.
func mergeEnv(base, overrides []string) []string {
	keys := make(map[string]bool)
	for _, override := range overrides {
		if key, _, ok := strings.Cut(override, "="); ok {
			keys[key] = true
		}
	}
	var merged []string
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok && keys[key] {
			continue
		}
		merged = append(merged, entry)
	}
	for _, override := range overrides {
		key, value, ok := strings.Cut(override, "=")
		if !ok {
			continue
		}
		if value == "" {
			continue
		}
		merged = append(merged, key+"="+value)
	}
	return merged
}

// suiteEnvAllowlist is the explicit environment contract for the gate's test
// processes. Ambient NATCATTY_* variables (notably NATCATTY_ET_UPDATE_GOLDEN)
// and other test controls are never inherited, so a developer shell or an
// unrelated CI setting cannot turn a golden regression into a golden rewrite,
// enable another lab endpoint, or otherwise mask a failure.
var suiteEnvAllowlist = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TMPDIR": true, "TMP": true, "TEMP": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "LC_MESSAGES": true,
	"SSH_AUTH_SOCK": true, "SSH_AGENT_PID": true,
	"GOROOT": true, "GOPATH": true, "GOCACHE": true, "GOMODCACHE": true,
	"GOFLAGS": true, "GOENV": true, "GOTOOLCHAIN": true, "GO111MODULE": true,
	"GOPROXY": true, "GOSUMDB": true, "GOPRIVATE": true, "GONOPROXY": true,
	"GONOSUMDB": true, "CGO_ENABLED": true, "CC": true, "CXX": true,
	"PKG_CONFIG_PATH": true,
}

// suiteEnv builds a test process environment from the allowlist and then
// applies the gate's own explicit overrides. It never passes through ambient
// NATCATTY_* or test-update variables.
func suiteEnv(base, overrides []string) []string {
	env := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if suiteEnvAllowlist[key] {
			env = append(env, entry)
		}
	}
	merged := mergeEnv(env, overrides)
	if merged == nil {
		// A nil Cmd.Env would inherit the parent environment, defeating the
		// allowlist; an empty, non-nil slice is an empty environment.
		merged = []string{}
	}
	return merged
}

// requireCleanWorktree refuses to produce a report from uncommitted changes.
// A dirty tree means the report cannot prove the checkout it names, even
// though the commit hash would match. Only untracked report artifacts under
// the -out directory are exempt, so a prior run in a non-gitignored -out
// directory does not fail the next cell. Tracked content there (source,
// fixtures, or checked-in reports) is never exempt: otherwise pointing -out
// at a tracked directory would let a report be produced from an unclean tree.
func requireCleanWorktree(root, outDir string) error {
	output, err := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return fmt.Errorf("check worktree cleanliness: %w", err)
	}
	relativeOut := ""
	if outDir != "" {
		if abs, err := filepath.Abs(outDir); err == nil {
			if rel, err := filepath.Rel(root, abs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				relativeOut = filepath.ToSlash(rel)
			}
		}
	}
	var dirty []string
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Porcelain v1 lines are `XY PATH`; XY is `??` for untracked and `!!`
		// for ignored entries. Ignored entries are not reported unless git
		// status is asked for them, but allowing them here keeps the exemption
		// correct if it ever is.
		status := ""
		if len(line) >= 2 {
			status = line[:2]
		}
		path := line
		if len(path) > 3 {
			path = path[3:]
		}
		if index := strings.Index(path, " -> "); index >= 0 {
			path = path[index+4:]
		}
		path = strings.Trim(path, `"`)
		if isReportArtifactStatus(status) && relativeOut != "" &&
			(path == relativeOut || strings.HasPrefix(path, relativeOut+"/")) {
			continue
		}
		dirty = append(dirty, line)
	}
	if len(dirty) > 0 {
		return fmt.Errorf("worktree is dirty; refusing to produce parity reports. Commit or clean:\n  %s",
			strings.Join(dirty, "\n  "))
	}
	return nil
}

// isReportArtifactStatus reports whether a git porcelain status entry is an
// untracked or ignored file, the only kinds of entry -out may contain.
func isReportArtifactStatus(status string) bool {
	return status == "??" || status == "!!"
}

func gitCommit(root string) string {
	cmd := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func gitTreeHash(root string) string {
	cmd := exec.Command("git", "-C", root, "rev-parse", "HEAD^{tree}")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

// resolveCellSelection returns the cells named by -cell, or the whole matrix
// for -all. Duplicate names are rejected.
func resolveCellSelection(ids []string, all bool) ([]Cell, error) {
	if all {
		if len(ids) > 0 {
			return nil, errors.New("-all and -cell are mutually exclusive")
		}
		return Matrix, nil
	}
	if len(ids) == 0 {
		return nil, errors.New("select a cell with -cell ID (repeatable) or -all")
	}
	seen := make(map[string]bool)
	var cells []Cell
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("cell %q repeated", id)
		}
		seen[id] = true
		cell, ok := findCell(id)
		if !ok {
			return nil, fmt.Errorf("unknown cell %q; run `etparity list`", id)
		}
		cells = append(cells, cell)
	}
	return cells, nil
}
