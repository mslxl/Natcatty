package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// fuzzTarget is one bounded `go test -fuzz` invocation required by the gate.
// Package uses the same relative path spelling as the workflow so receipts can
// be compared directly with the compiled-in table.
type fuzzTarget struct {
	Package string
	Name    string
}

func (t fuzzTarget) key() string { return t.Package + ":" + t.Name }

// requiredFuzzTargets is the authoritative fuzz set: framing, secretbox,
// packet decoding, and replay handling. It is also referenced by Behaviors, so
// a renamed or deleted target fails verification even if a receipt still names
// it.
var requiredFuzzTargets = []fuzzTarget{
	{Package: "./adapters/et/internal/etgo/wire", Name: "FuzzReadFrame"},
	{Package: "./adapters/et/internal/etgo/seal", Name: "FuzzOpen"},
	{Package: "./adapters/et/internal/etgo/wire", Name: "FuzzParsePacket"},
	{Package: "./adapters/et/internal/etgo/etcp", Name: "FuzzReplayHandling"},
}

const (
	// fuzzReceiptSchema is bumped whenever receipt semantics change. verify
	// refuses receipts from another schema instead of guessing at their
	// meaning.
	fuzzReceiptSchema = 1
	// minFuzzTime is the documented minimum -fuzztime. Go's fuzzer needs a
	// bounded, non-zero fuzzing window; receipts below this are rejected by
	// verify even if they claim success. CI uses the 30s default.
	minFuzzTime = time.Second
	// defaultFuzzTime bounds each target in CI. It is a flag rather than a
	// waiver, and verify refuses anything below minFuzzTime.
	defaultFuzzTime = 30 * time.Second
	// defaultFuzzTimeout is the go test -timeout passed to each target.
	defaultFuzzTimeout = 10 * time.Minute
	// fuzzOutputLimit bounds a receipt's captured output the same way cell
	// reports bound suite output.
	fuzzOutputLimit = 256 << 10
)

// fuzzReceipt is the artifact-backed evidence for one required fuzz target.
// Status is not trusted on its own: verify recomputes it from Output and
// recomputes elapsed time from StartedAt/EndedAt. Commit, tree, run ID, host
// platform and fuzztime bind the receipt to the checkout and invocation.
//
// Receipts, like cell reports, are unsigned JSON. The real control is the
// workflow and artifact trust boundary: CI produces receipts in the fuzz job
// and verify requires them, so removing, commenting out, or skipping a fuzz
// step leaves no receipt and fails the gate closed.
type fuzzReceipt struct {
	Schema    int           `json:"schema"`
	Target    string        `json:"target"`
	Package   string        `json:"package"`
	Name      string        `json:"name"`
	Status    string        `json:"status"`
	Command   []string      `json:"command"`
	Output    string        `json:"output"`
	StartedAt time.Time     `json:"startedAt"`
	EndedAt   time.Time     `json:"endedAt"`
	FuzzTime  time.Duration `json:"fuzzTime"`
	HostOS    string        `json:"hostOS"`
	HostArch  string        `json:"hostArch"`
	Commit    string        `json:"commit"`
	TreeHash  string        `json:"treeHash"`
	RunID     string        `json:"runID,omitempty"`
}

// fuzzReceiptFile pairs a receipt with the artifact filename it was read from,
// so verify can reject a renamed or substituted file.
type fuzzReceiptFile struct {
	FileName string
	Receipt  fuzzReceipt
}

// goTestRunner executes one `go test` invocation and returns its combined
// stdout/stderr. It is injectable so receipt-generation tests exercise pass,
// failure, and preflight paths without running the fuzzer.
type goTestRunner func(ctx context.Context, dir string, args []string) ([]byte, error)

// fuzzOptions configures one `etparity fuzz` invocation.
type fuzzOptions struct {
	root     string
	outDir   string
	fuzzTime time.Duration
	timeout  time.Duration
	runner   goTestRunner
	now      func() time.Time
}

func (o fuzzOptions) normalized() fuzzOptions {
	if o.fuzzTime == 0 {
		o.fuzzTime = defaultFuzzTime
	}
	if o.timeout == 0 {
		o.timeout = defaultFuzzTimeout
	}
	if o.runner == nil {
		o.runner = runRealGoTest
	}
	if o.now == nil {
		o.now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

func runRealGoTest(ctx context.Context, dir string, args []string) ([]byte, error) {
	goBin, err := goBinary()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// fuzzTargetList is a repeatable -target flag.
type fuzzTargetList []string

func (l *fuzzTargetList) String() string { return fmt.Sprint([]string(*l)) }

func (l *fuzzTargetList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// fuzzCommand runs one or more required fuzz targets and writes one receipt
// per target. Selection is closed over requiredFuzzTargets: an unknown target
// is an error, not an extra run.
func fuzzCommand(args []string) error {
	flags := newFlagSet("fuzz")
	var specs fuzzTargetList
	flags.Var(&specs, "target", "required fuzz target as PKG:NAME (repeatable)")
	out := flags.String("out", filepath.Join("build", "et-parity"), "receipt directory")
	fuzzTime := flags.Duration("fuzztime", defaultFuzzTime,
		fmt.Sprintf("bounded fuzzing time per target (minimum %s)", minFuzzTime))
	timeout := flags.Duration("timeout", defaultFuzzTimeout, "per-target go test timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *fuzzTime < minFuzzTime {
		return fmt.Errorf("-fuzztime %s is below the documented minimum %s", *fuzzTime, minFuzzTime)
	}
	if *timeout <= 0 {
		return errors.New("-timeout must be positive")
	}
	targets, err := resolveFuzzTargets(specs)
	if err != nil {
		return err
	}
	root, err := findRoot()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*out) {
		*out = filepath.Join(root, *out)
	}
	// A receipt produced from uncommitted code cannot prove the checkout it
	// names. Cell reports use the same rule; -out is exempt as an artifact
	// directory.
	if err := requireCleanWorktree(root, *out); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	receipts, runErr := runFuzzTargets(ctx, targets, fuzzOptions{
		root:     root,
		outDir:   *out,
		fuzzTime: *fuzzTime,
		timeout:  *timeout,
	})
	for _, receipt := range receipts {
		fmt.Fprintf(os.Stdout, "%-32s %-5s %s\n", receipt.Target,
			strings.ToUpper(receipt.Status),
			receipt.EndedAt.Sub(receipt.StartedAt).Round(time.Millisecond))
	}
	if runErr != nil {
		return fmt.Errorf("fuzzing failed; receipts are in %s: %w", *out, runErr)
	}
	fmt.Printf("\nall %d fuzz targets passed; receipts are in %s\n", len(receipts), *out)
	return nil
}

// resolveFuzzTargets parses PKG:NAME specs and requires every one to be in the
// compiled-in table. The canonical table entry is returned so a receipt can
// never name a differently-spelled package or target.
func resolveFuzzTargets(specs []string) ([]fuzzTarget, error) {
	if len(specs) == 0 {
		return nil, errors.New("select at least one required fuzz target with -target PKG:NAME")
	}
	required := make(map[string]fuzzTarget, len(requiredFuzzTargets))
	for _, target := range requiredFuzzTargets {
		required[target.key()] = target
	}
	seen := make(map[string]bool, len(specs))
	targets := make([]fuzzTarget, 0, len(specs))
	for _, spec := range specs {
		parsed, err := parseFuzzTargetSpec(spec)
		if err != nil {
			return nil, err
		}
		target, ok := required[parsed.key()]
		if !ok {
			return nil, fmt.Errorf("fuzz target %s is not in the required fuzz table (%s)",
				parsed.key(), strings.Join(requiredFuzzKeys(), ", "))
		}
		if seen[target.key()] {
			return nil, fmt.Errorf("fuzz target %s selected more than once", target.key())
		}
		seen[target.key()] = true
		targets = append(targets, target)
	}
	return targets, nil
}

func parseFuzzTargetSpec(spec string) (fuzzTarget, error) {
	pkg, name, ok := strings.Cut(strings.TrimSpace(spec), ":")
	pkg, name = strings.TrimSpace(pkg), strings.TrimSpace(name)
	if !ok || pkg == "" || name == "" {
		return fuzzTarget{}, fmt.Errorf("fuzz target %q must be PKG:NAME, for example ./pkg:FuzzThing", spec)
	}
	return fuzzTarget{Package: pkg, Name: name}, nil
}

func requiredFuzzKeys() []string {
	keys := make([]string, 0, len(requiredFuzzTargets))
	for _, target := range requiredFuzzTargets {
		keys = append(keys, target.key())
	}
	sort.Strings(keys)
	return keys
}

// runFuzzTargets runs each target and writes its receipt atomically, even when
// the source check, preflight, or fuzz invocation fails. A failure in one
// target does not stop the others from producing evidence.
func runFuzzTargets(ctx context.Context, targets []fuzzTarget, opts fuzzOptions) ([]fuzzReceipt, error) {
	opts = opts.normalized()
	if opts.fuzzTime < minFuzzTime {
		return nil, fmt.Errorf("-fuzztime %s is below the documented minimum %s", opts.fuzzTime, minFuzzTime)
	}
	if len(targets) == 0 {
		return nil, errors.New("no fuzz targets selected")
	}
	receipts := make([]fuzzReceipt, 0, len(targets))
	var problems []string
	seen := make(map[string]bool, len(targets))
	for _, target := range targets {
		if seen[target.key()] {
			return receipts, fmt.Errorf("fuzz target %s selected more than once", target.key())
		}
		seen[target.key()] = true
		receipt, err := runFuzzTarget(ctx, target, opts)
		if writeErr := writeFuzzReceipt(opts.outDir, receipt); writeErr != nil {
			problems = append(problems, writeErr.Error())
			if err != nil {
				problems = append(problems, err.Error())
			}
			continue
		}
		receipts = append(receipts, receipt)
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return receipts, errors.New(strings.Join(problems, "\n"))
	}
	return receipts, nil
}

// runFuzzTarget applies the source check, the exact-match `-list` preflight,
// and the bounded fuzz run. The returned receipt is always written by the
// caller, including on failure.
func runFuzzTarget(ctx context.Context, target fuzzTarget, opts fuzzOptions) (fuzzReceipt, error) {
	receipt := newFuzzReceipt(target, opts)
	if err := targetDefined(opts.root, target); err != nil {
		now := opts.now()
		receipt.StartedAt, receipt.EndedAt = now, now
		receipt.Output = err.Error()
		return receipt, err
	}

	listArgv := fuzzListArgs(target)
	listOutput, listErr := runGoTest(ctx, opts, listArgv)
	if listErr != nil || !listOutputContains(listOutput, target.Name) {
		receipt.StartedAt, receipt.EndedAt = opts.now(), opts.now()
		receipt.Output = truncate(fuzzPreflightDetail(listArgv, listOutput, listErr), fuzzOutputLimit)
		if listErr != nil {
			return receipt, fmt.Errorf("fuzz target %s: -list preflight failed: %w", target.key(), listErr)
		}
		return receipt, fmt.Errorf("fuzz target %s: -list preflight did not report an exact %q line",
			target.key(), target.Name)
	}

	receipt.StartedAt = opts.now()
	output, runErr := runGoTest(ctx, opts, receipt.Command)
	receipt.EndedAt = opts.now()
	text := string(output)
	if text == "" && runErr != nil {
		text = runErr.Error()
	}
	receipt.Output = truncate(text, fuzzOutputLimit)
	if runErr != nil {
		return receipt, fmt.Errorf("fuzz target %s: %w", target.key(), runErr)
	}
	if problems := fuzzOutputProblems(receipt.Output); len(problems) > 0 {
		return receipt, fmt.Errorf("fuzz target %s: %s", target.key(), strings.Join(problems, "; "))
	}
	receipt.Status = "pass"
	return receipt, nil
}

func newFuzzReceipt(target fuzzTarget, opts fuzzOptions) fuzzReceipt {
	return fuzzReceipt{
		Schema:   fuzzReceiptSchema,
		Target:   target.key(),
		Package:  target.Package,
		Name:     target.Name,
		Status:   "fail",
		Command:  fuzzRunArgs(target, opts),
		FuzzTime: opts.fuzzTime,
		HostOS:   runtime.GOOS,
		HostArch: runtime.GOARCH,
		Commit:   gitCommit(opts.root),
		TreeHash: gitTreeHash(opts.root),
		RunID:    strings.TrimSpace(os.Getenv("GITHUB_RUN_ID")),
	}
}

func fuzzListArgs(target fuzzTarget) []string {
	return []string{"test", target.Package, "-list", "^" + target.Name + "$"}
}

func fuzzRunArgs(target fuzzTarget, opts fuzzOptions) []string {
	return []string{
		"test", target.Package,
		"-run", "^$",
		"-fuzz", "^" + target.Name + "$",
		"-fuzztime", opts.fuzzTime.String(),
		"-timeout", opts.timeout.String(),
	}
}

func runGoTest(ctx context.Context, opts fuzzOptions, args []string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, opts.timeout+2*time.Minute)
	defer cancel()
	output, err := opts.runner(runCtx, opts.root, args)
	if err != nil && ctx.Err() != nil {
		return output, ctx.Err()
	}
	return output, err
}

// listOutputContains reports whether `go test -list` printed exactly the named
// target. Substring matching would accept FuzzReadFrameExtra or a package
// name that merely looks like the target.
func listOutputContains(output []byte, name string) bool {
	for _, line := range strings.Split(normalizeNewlines(string(output)), "\n") {
		if line == name {
			return true
		}
	}
	return false
}

func fuzzPreflightDetail(argv []string, output []byte, err error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "preflight: go %s\n", strings.Join(argv, " "))
	if len(output) > 0 {
		b.Write(output)
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
	}
	if err != nil {
		fmt.Fprintf(&b, "preflight error: %v\n", err)
	}
	return b.String()
}

func normalizeNewlines(text string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(text)
}

// fuzzOutputProblems is the evidence verify recomputes instead of trusting a
// receipt's Status. A successful bounded run prints fuzzing progress, PASS,
// and the go test package result. A receipt carrying only a -list preflight,
// a compile failure, or a crashed fuzz invocation cannot pass.
func fuzzOutputProblems(output string) []string {
	var problems []string
	sawFuzzProgress, sawPass, sawOK := false, false, false
	for _, line := range strings.Split(normalizeNewlines(output), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "fuzz: elapsed:"):
			sawFuzzProgress = true
		case line == "PASS":
			sawPass = true
		case line == "FAIL", strings.HasPrefix(line, "--- FAIL:"),
			strings.HasPrefix(line, "panic:"):
			problems = append(problems, fmt.Sprintf("captured output records a failure: %s", line))
		case len(line) >= 3 && line[:2] == "ok" && (line[2] == ' ' || line[2] == '\t'):
			sawOK = true
		}
	}
	if !sawFuzzProgress {
		problems = append(problems, "captured output has no fuzzing progress (fuzz: elapsed)")
	}
	if !sawPass {
		problems = append(problems, "captured output has no PASS line")
	}
	if !sawOK {
		problems = append(problems, "captured output has no successful go test package line")
	}
	return problems
}

func fuzzReceiptPath(dir, name string) string {
	return filepath.Join(dir, "fuzz-"+name+".json")
}

// writeFuzzReceipt publishes a receipt atomically: a concurrent verifier or
// artifact uploader sees either the complete JSON file or no file at all.
func writeFuzzReceipt(dir string, receipt fuzzReceipt) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := fuzzReceiptPath(dir, receipt.Name)
	temp, err := os.CreateTemp(dir, ".fuzz-receipt-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	tempName = ""
	return nil
}

// loadFuzzReceipts reads every `fuzz-*.json` artifact in the merged report
// directory. Cell reports are handled by loadReports; the two artifact sets
// share a directory in CI.
func loadFuzzReceipts(dir string) ([]fuzzReceiptFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var receipts []fuzzReceiptFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "fuzz-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		var receipt fuzzReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		receipts = append(receipts, fuzzReceiptFile{FileName: name, Receipt: receipt})
	}
	return receipts, nil
}

// verifyFuzzReceipts is the artifact-backed fail-closed check. It requires the
// exact required target set (missing, extra, duplicate, wrong name or package
// all fail), then validates each receipt's schema, command, status, captured
// output, commit/tree, run ID, freshness, fuzztime, elapsed time, host
// provenance, and the target's continued existence in source.
func verifyFuzzReceipts(root string, required []fuzzTarget, receipts []fuzzReceiptFile, opts gateOptions) error {
	opts = opts.normalized()
	var problems []string
	requiredByKey := make(map[string]fuzzTarget, len(required))
	for _, target := range required {
		requiredByKey[target.key()] = target
	}
	byKey := make(map[string][]fuzzReceiptFile, len(receipts))
	for _, file := range receipts {
		key := file.Receipt.Package + ":" + file.Receipt.Name
		byKey[key] = append(byKey[key], file)
	}
	for key, files := range byKey {
		if len(files) > 1 {
			names := make([]string, 0, len(files))
			for _, file := range files {
				names = append(names, file.FileName)
			}
			problems = append(problems, fmt.Sprintf("fuzz target %s has duplicate receipts: %s",
				key, strings.Join(names, ", ")))
		}
		target, ok := requiredByKey[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("fuzz receipt %s names %s, which is not a required fuzz target",
				files[0].FileName, key))
			continue
		}
		for _, file := range files {
			problems = append(problems, verifyFuzzReceipt(root, target, file, opts)...)
		}
	}
	for _, target := range required {
		if len(byKey[target.key()]) == 0 {
			problems = append(problems, fmt.Sprintf(
				"required fuzz target %s has no receipt (the fuzz step did not run, was skipped, or its artifact is missing)",
				target.key()))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New("ET parity gate FAILED:\n  " + strings.Join(problems, "\n  "))
	}
	return nil
}

func verifyFuzzReceipt(root string, target fuzzTarget, file fuzzReceiptFile, opts gateOptions) []string {
	var problems []string
	receipt := file.Receipt
	id := "fuzz " + target.key()
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if receipt.Schema != fuzzReceiptSchema {
		add("%s: receipt schema %d, want %d", id, receipt.Schema, fuzzReceiptSchema)
	}
	if receipt.Target != target.key() {
		add("%s: receipt target %q, want %q", id, receipt.Target, target.key())
	}
	if receipt.Package != target.Package {
		add("%s: receipt package %q, want %q", id, receipt.Package, target.Package)
	}
	if receipt.Name != target.Name {
		add("%s: receipt name %q, want %q", id, receipt.Name, target.Name)
	}
	if want := "fuzz-" + target.Name + ".json"; file.FileName != want {
		add("%s: receipt file %q, want %q", id, file.FileName, want)
	}
	if receipt.Status != "pass" {
		add("%s: receipt status %q", id, receipt.Status)
	}
	problems = append(problems, verifyFuzzCommand(id, receipt.Command, target, receipt.FuzzTime)...)

	if strings.TrimSpace(receipt.Output) == "" {
		add("%s: receipt recorded no captured output", id)
	} else {
		for _, problem := range fuzzOutputProblems(receipt.Output) {
			add("%s: %s", id, problem)
		}
	}
	if receipt.HostOS == "" || receipt.HostArch == "" {
		add("%s: receipt has no host platform provenance (hostOS/hostArch)", id)
	}
	if receipt.Commit != opts.Commit {
		add("%s: receipt commit %q, want %q", id, receipt.Commit, opts.Commit)
	}
	if receipt.TreeHash != opts.TreeHash {
		add("%s: receipt tree %q, want %q", id, receipt.TreeHash, opts.TreeHash)
	}
	switch {
	case opts.RunID != "" && receipt.RunID == "":
		add("%s: receipt is not bound to CI run %q", id, opts.RunID)
	case opts.RunID != "" && receipt.RunID != opts.RunID:
		add("%s: receipt CI run %q, want %q", id, receipt.RunID, opts.RunID)
	case opts.RunID == "" && receipt.RunID != "":
		add("%s: receipt is bound to CI run %q but verify has no GITHUB_RUN_ID", id, receipt.RunID)
	}

	if receipt.FuzzTime < minFuzzTime {
		add("%s: fuzztime %s is below the documented minimum %s", id, receipt.FuzzTime, minFuzzTime)
	}
	if receipt.StartedAt.IsZero() {
		add("%s: receipt has no start time", id)
	}
	if receipt.EndedAt.IsZero() {
		add("%s: receipt has no end time", id)
	}
	if !receipt.StartedAt.IsZero() && !receipt.EndedAt.IsZero() {
		start, end := receipt.StartedAt.UTC(), receipt.EndedAt.UTC()
		elapsed := end.Sub(start)
		if elapsed < 0 {
			add("%s: receipt ends before it starts", id)
		}
		// Elapsed is recomputed from the recorded timestamps; a self-reported
		// pass cannot shorten the fuzzing window.
		if elapsed < receipt.FuzzTime {
			add("%s: fuzz run elapsed %s is shorter than fuzztime %s",
				id, elapsed.Round(time.Millisecond), receipt.FuzzTime)
		}
		for _, stamp := range []struct {
			label string
			at    time.Time
		}{{"start", start}, {"end", end}} {
			age := opts.Now.Sub(stamp.at)
			if age > opts.MaxAge {
				add("%s: receipt %s time is stale (generated %s ago, limit %s)",
					id, stamp.label, age.Round(time.Second), opts.MaxAge)
			}
			if age < -reportClockSkew {
				add("%s: receipt %s time is %s in the future",
					id, stamp.label, (-age).Round(time.Second))
			}
		}
	}

	if err := targetDefined(root, target); err != nil {
		add("%s: %v", id, err)
	}
	return problems
}

// verifyFuzzCommand requires the recorded command to be the bounded fuzz
// invocation for this exact target and fuzztime. It is belt-and-braces with
// the target fields: a receipt cannot say "pass" for one target while its
// command ran another.
func verifyFuzzCommand(id string, argv []string, target fuzzTarget, fuzzTime time.Duration) []string {
	if len(argv) < 2 || argv[0] != "test" || argv[1] != target.Package {
		return []string{fmt.Sprintf("%s: receipt command %v does not run `go test %s`",
			id, argv, target.Package)}
	}
	values := make(map[string]string)
	for i := 2; i+1 < len(argv); i++ {
		if strings.HasPrefix(argv[i], "-") {
			values[argv[i]] = argv[i+1]
			i++
		}
	}
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	if values["-run"] != "^$" {
		add("%s: receipt command -run %q, want %q", id, values["-run"], "^$")
	}
	if want := "^" + target.Name + "$"; values["-fuzz"] != want {
		add("%s: receipt command -fuzz %q, want %q", id, values["-fuzz"], want)
	}
	recorded, err := time.ParseDuration(values["-fuzztime"])
	if err != nil {
		add("%s: receipt command has no valid -fuzztime: %v", id, err)
	} else if recorded != fuzzTime {
		add("%s: receipt command fuzztime %s, receipt declares %s", id, recorded, fuzzTime)
	}
	if timeout, ok := values["-timeout"]; ok {
		if _, err := time.ParseDuration(timeout); err != nil {
			add("%s: receipt command has invalid -timeout %q", id, timeout)
		}
	}
	return problems
}

func printFuzzSummary(w io.Writer, receipts []fuzzReceiptFile) {
	fmt.Fprintf(w, "\n%-32s %-8s %-10s %s\n", "FUZZ TARGET", "STATUS", "FUZZTIME", "DURATION")
	for _, target := range requiredFuzzTargets {
		printed := false
		for _, file := range receipts {
			receipt := file.Receipt
			if receipt.Package+":"+receipt.Name != target.key() {
				continue
			}
			printed = true
			fmt.Fprintf(w, "%-32s %-8s %-10s %s\n", target.key(),
				strings.ToUpper(receipt.Status), receipt.FuzzTime,
				receipt.EndedAt.Sub(receipt.StartedAt).Round(time.Millisecond))
		}
		if !printed {
			fmt.Fprintf(w, "%-32s %-8s %-10s %s\n", target.key(), "MISSING", "-", "-")
		}
	}
}

// targetDefined reports whether the named fuzz function exists in the
// package's test source. It parses the source with go/parser so a
// commented-out or stringified signature cannot stand in for a real fuzz
// target that Go would compile and list.
func targetDefined(root string, target fuzzTarget) error {
	if !strings.HasPrefix(target.Package, "./") || strings.Contains(target.Package, "..") {
		return fmt.Errorf("fuzz target %s has an invalid package path", target.key())
	}
	dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(target.Package, "./")))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("fuzz target %s: read package: %w", target.key(), err)
	}
	found := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("fuzz target %s: read %s: %w", target.key(), entry.Name(), err)
		}
		defined, err := declaresFuzzTarget(data, target.Name)
		if err != nil {
			return fmt.Errorf("fuzz target %s: parse %s: %w", target.key(), entry.Name(), err)
		}
		if defined {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("fuzz target %s has no `func %s(f *testing.F)` in %s", target.key(), target.Name, target.Package)
	}
	return nil
}

// declaresFuzzTarget reports whether data declares a top-level
// `func Name(*testing.F)`. It parses the source instead of matching text, so
// a commented-out or stringified signature cannot stand in for a real fuzz
// target that Go would actually compile and list.
func declaresFuzzTarget(data []byte, name string) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "", data, parser.SkipObjectResolution)
	if err != nil {
		return false, err
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != name {
			continue
		}
		params := fn.Type.Params.List
		if len(params) != 1 || len(params[0].Names) > 1 {
			continue
		}
		star, ok := params[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		selector, ok := star.X.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "F" {
			continue
		}
		if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "testing" {
			return true, nil
		}
	}
	return false, nil
}
