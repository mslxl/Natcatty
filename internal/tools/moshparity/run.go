// SPDX-License-Identifier: GPL-3.0-or-later

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
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const maxSavedOutput = 256 << 10

// Disposable lab credentials created by adapters/mosh/lab. The gate
// passes them to the integration tests and uses them to probe a native
// endpoint's actual platform before writing a report.
const (
	labSSHUser     = "tester"
	labSSHPassword = "testpass"
)

type runOptions struct {
	root     string
	labDir   string
	outDir   string
	commit   string
	keep     bool
	verbose  bool
	build    bool
	timeout  time.Duration
	labAddr  string
	fuzzTime time.Duration
}

// suiteSpec is one `go test -json` invocation in a cell.
type suiteSpec struct {
	name     string
	packages []string
	env      []string
	run      string
}

// fuzzSpec is one bounded native fuzz run.
type fuzzSpec struct {
	pkg    string
	target string
}

var fuzzTargets = []fuzzSpec{
	{pkg: "./internal/mosh", target: "FuzzDatagramReceive"},
	{pkg: "./internal/mosh", target: "FuzzFragmentReassembly"},
	{pkg: "./internal/mosh", target: "FuzzTransportInstruction"},
	{pkg: "./internal/mosh/terminal", target: "FuzzTerminalParser"},
	{pkg: "./domain/model/mosh", target: "FuzzAnnouncementParser"},
}

func runCell(ctx context.Context, cell Cell, opts runOptions) (CellReport, error) {
	if strings.TrimSpace(opts.commit) == "" {
		return CellReport{}, fmt.Errorf("cell %s: no gate commit; pass -commit or set %s", cell.ID, commitEnv)
	}
	if cell.Kind != kindStock && opts.fuzzTime < minReportFuzzTime {
		return CellReport{}, fmt.Errorf("cell %s: -fuzztime %s is below the %s gate minimum", cell.ID, opts.fuzzTime, minReportFuzzTime)
	}
	if cell.Kind != kindStock && opts.fuzzTime > maxReportFuzzTime {
		return CellReport{}, fmt.Errorf("cell %s: -fuzztime %s exceeds the %s gate maximum", cell.ID, opts.fuzzTime, maxReportFuzzTime)
	}
	if opts.labAddr != "" && !cell.IsNative() {
		return CellReport{}, fmt.Errorf("cell %s: -lab-addr is only valid for native cells", cell.ID)
	}
	report := CellReport{
		Schema:        reportSchema,
		Cell:          cell,
		GeneratedAt:   time.Now().UTC(),
		Commit:        opts.commit,
		GoVersion:     runtime.Version(),
		ServerVersion: cell.Version,
		HostOS:        runtime.GOOS,
		HostArch:      runtime.GOARCH,
		HostUname:     hostUname(),
		FuzzTime:      opts.fuzzTime.String(),
	}
	labAddr := cell.SSHAddr
	if opts.labAddr != "" {
		labAddr = opts.labAddr
	}
	report.LabAddress = labAddr

	switch cell.Kind {
	case kindStock:
		if err := ensureStockImage(ctx, cell, opts); err != nil {
			return finish(report, opts, fmt.Errorf("build stock parity image for %s: %w", cell.ID, err))
		}
		if err := recordImagePlatform(ctx, cell, &report); err != nil {
			return finish(report, opts, err)
		}
	case kindDocker:
		if err := ensureImage(ctx, cell, opts); err != nil {
			return finish(report, opts, fmt.Errorf("build image for %s: %w", cell.ID, err))
		}
		if err := recordImagePlatform(ctx, cell, &report); err != nil {
			return finish(report, opts, err)
		}
		if err := startDockerCell(ctx, cell, opts); err != nil {
			saveContainerLogs(cell, opts, "start")
			stopDockerCell(cell)
			return finish(report, opts, fmt.Errorf("start %s: %w", cell.ID, err))
		}
		if !opts.keep {
			defer stopDockerCell(cell)
		}
		if err := waitTCP(ctx, cell.SSHAddr, 3*time.Minute); err != nil {
			saveContainerLogs(cell, opts, "startup")
			return finish(report, opts, fmt.Errorf("cell %s SSH endpoint %s: %w", cell.ID, cell.SSHAddr, err))
		}
	default:
		// A native cell may only be run on its own OS. Assert before touching
		// the endpoint so a macos/* or freebsd-14/* report cannot be produced
		// from a host that merely happens to run an sshd.
		if err := assertNativeHost(cell); err != nil {
			return finish(report, opts, err)
		}
		if err := waitTCP(ctx, labAddr, 60*time.Second); err != nil {
			return finish(report, opts, fmt.Errorf("native cell %s is not listening at %s: %w", cell.ID, labAddr, err))
		}
		// Record the endpoint's actual platform, not only the harness OS:
		// -lab-addr may point at another host, and verify binds the report to
		// the platform that really ran the suites.
		labOS, labArch, err := remotePlatform(ctx, labAddr)
		if err != nil {
			return finish(report, opts, fmt.Errorf("native cell %s: probe platform at %s: %w", cell.ID, labAddr, err))
		}
		if !strings.EqualFold(labOS, cell.ExpectedLabOS()) {
			return finish(report, opts, fmt.Errorf("native cell %s: lab at %s reports %q, want %q", cell.ID, labAddr, labOS, cell.ExpectedLabOS()))
		}
		report.LabOS = labOS
		report.LabArch = labArch
	}

	suites := cellSuites(cell, labAddr, opts)
	for _, suite := range suites {
		result, err := runSuite(ctx, opts, suite)
		report.Suites = append(report.Suites, result)
		if err != nil && ctx.Err() != nil {
			return finish(report, opts, ctx.Err())
		}
		// Continue to the remaining suites so the report contains every
		// failure, not only the first.
	}
	if cell.Kind != kindStock {
		fuzzSuite, err := runFuzzSuite(ctx, opts)
		report.Suites = append(report.Suites, fuzzSuite)
		if err != nil && ctx.Err() != nil {
			return finish(report, opts, ctx.Err())
		}
	}
	if cell.Kind == kindDocker && !opts.keep {
		saveContainerLogs(cell, opts, "final")
	}

	report.Behaviors = evaluateCell(report)
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

// cellSuites returns the ordered suites a cell must run. Stock cells run only
// the differential; every server cell runs the hermetic suite, the real-server
// lab suite, and a bounded fuzz suite.
func cellSuites(cell Cell, labAddr string, opts runOptions) []suiteSpec {
	if cell.Kind == kindStock {
		return []suiteSpec{{
			name:     suiteStock,
			packages: []string{"./adapters/mosh", "./internal/mosh/terminal"},
			env: envWith(os.Environ(), []string{
				"NATCATTY_MOSH_PARITY=1",
				"NATCATTY_MOSH_PARITY_IMAGE=" + cell.Image,
				"NATCATTY_MOSH_INTEGRATION=",
				"NATCATTY_MOSH_LOCAL_SERVER=",
			}),
			run: `^(TestSessionMatchesStockMoshClient|TestSessionMatchesStockMoshClientWidthEdges|TestStockParityTerminalModel|TestStockParityRandom)$`,
		}}
	}

	host := cell.SSHHost()
	if opts.labAddr != "" {
		if parsedHost, _, err := net.SplitHostPort(opts.labAddr); err == nil {
			host = parsedHost
		}
	}
	sshPort := cell.SSHHostPort()
	if opts.labAddr != "" {
		if _, parsedPort, err := net.SplitHostPort(opts.labAddr); err == nil {
			if value, convErr := strconv.Atoi(parsedPort); convErr == nil {
				sshPort = value
			}
		}
	}
	labEnv := envWith(os.Environ(), []string{
		"NATCATTY_MOSH_INTEGRATION=1",
		"NATCATTY_MOSH_SSH_HOST=" + host,
		"NATCATTY_MOSH_SSH_USER=" + labSSHUser,
		"NATCATTY_MOSH_SSH_PASSWORD=" + labSSHPassword,
		"NATCATTY_MOSH_SERVER_VERSION=" + cell.Version,
		"NATCATTY_MOSH_EXPECT_VERSION=" + cell.Version,
		"NATCATTY_MOSH_SSH_PORT=" + fmt.Sprint(sshPort),
		"NATCATTY_MOSH_SERVER_PATH=" + cell.ServerPath,
		"NATCATTY_MOSH_UDP_PORT=" + fmt.Sprint(cell.UDPPort),
		"NATCATTY_MOSH_LOCAL_SERVER=",
		"NATCATTY_MOSH_PARITY=",
	})
	labRun := `^(TestInProcessAdapterRealMoshServer|TestInProcessAdapterLabServerVersion|TestInProcessAdapterLabLocalePropagation|TestInProcessAdapterLabResizeAndShutdown`
	if cell.Version == v140 {
		labRun += `|TestInProcessAdapterRealMoshRoaming`
	}
	labRun += `)$`

	return []suiteSpec{
		{
			name:     suiteUnit,
			packages: []string{"./internal/mosh/...", "./domain/model/mosh", "./adapters/mosh"},
			env: envWith(os.Environ(), []string{
				"NATCATTY_MOSH_INTEGRATION=",
				"NATCATTY_MOSH_PARITY=",
				"NATCATTY_MOSH_LOCAL_SERVER=",
			}),
			run: `^(Test|Fuzz)`,
		},
		{
			name:     suiteLab,
			packages: []string{"./adapters/mosh"},
			env:      labEnv,
			run:      labRun,
		},
	}
}

func runSuite(ctx context.Context, opts runOptions, spec suiteSpec) (SuiteResult, error) {
	result := SuiteResult{Name: spec.name, Packages: spec.packages}
	args := []string{"test", "-json", "-count=1", "-timeout", opts.timeout.String()}
	if spec.run != "" {
		args = append(args, "-run", spec.run)
	}
	args = append(args, spec.packages...)
	goBin, err := goBinary()
	if err != nil {
		result.Status = "fail"
		result.Output = err.Error()
		return result, err
	}
	suiteCtx, cancel := context.WithTimeout(ctx, opts.timeout+2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(suiteCtx, goBin, args...)
	cmd.Dir = opts.root
	cmd.Env = spec.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	runErr := cmd.Run()
	result.Elapsed = time.Since(started)
	tests, parseErr := parseGoTestJSON(bytes.NewReader(stdout.Bytes()))
	result.Tests = tests
	output := append(append([]byte(nil), stdout.Bytes()...), stderr.Bytes()...)
	result.Output = limitOutput(output)
	result.Status = "pass"
	if runErr != nil || parseErr != nil {
		result.Status = "fail"
	}
	if suiteCtx.Err() == context.DeadlineExceeded {
		result.Status = "fail"
		result.Output = result.Output + "\nsuite exceeded its timeout"
	}
	return result, runErr
}

func runFuzzSuite(ctx context.Context, opts runOptions) (SuiteResult, error) {
	result := SuiteResult{Name: suiteFuzz}
	started := time.Now()
	var failures []string
	for _, target := range fuzzTargets {
		if opts.fuzzTime <= 0 {
			failures = append(failures, fmt.Sprintf("%s: fuzzing disabled", target.target))
			continue
		}
		goBin, err := goBinary()
		if err != nil {
			result.Status = "fail"
			result.Output = err.Error()
			result.Elapsed = time.Since(started)
			return result, err
		}
		args := []string{"test", "-run=^$", "-fuzz=^" + target.target + "$",
			"-fuzztime=" + opts.fuzzTime.String(), target.pkg}
		fuzzCtx, cancel := context.WithTimeout(ctx, opts.fuzzTime+3*time.Minute)
		cmd := exec.CommandContext(fuzzCtx, goBin, args...)
		cmd.Dir = opts.root
		cmd.Env = envWith(os.Environ(), []string{
			"NATCATTY_MOSH_INTEGRATION=", "NATCATTY_MOSH_PARITY=",
			"NATCATTY_MOSH_LOCAL_SERVER=",
		})
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		err = cmd.Run()
		cancel()
		// `go test -fuzz` exits zero with "no targets to fuzz" when the
		// target was renamed or removed. Without this check the gate would
		// silently accept a deleted fuzz target.
		if err == nil && !bytes.Contains(output.Bytes(), []byte("fuzz: elapsed:")) {
			err = fmt.Errorf("go test -fuzz=%s produced no fuzz run", target.target)
		}
		result.Packages = append(result.Packages, target.pkg)
		result.Output = limitOutput(append([]byte(result.Output), output.Bytes()...))
		action := "pass"
		if err != nil {
			action = "fail"
			failures = append(failures, fmt.Sprintf("%s: %v", target.target, err))
		}
		result.Tests = append(result.Tests, TestResult{
			Package: packagePath(target.pkg), Name: target.target, Action: action,
		})
	}
	result.Elapsed = time.Since(started)
	result.Status = "pass"
	if len(failures) > 0 {
		result.Status = "fail"
		result.Output += "\n" + strings.Join(failures, "\n")
	}
	return result, nil
}

func packagePath(dir string) string {
	trimmed := strings.TrimPrefix(dir, "./")
	return modulePath + "/" + trimmed
}

func finish(report CellReport, opts runOptions, err error) (CellReport, error) {
	report.Status = "fail"
	if report.Behaviors == nil {
		report.Behaviors = evaluateCell(report)
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
			fmt.Fprintf(w, "    %-6s %s/%s: %s (%s)\n",
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

func limitOutput(data []byte) string {
	const keep = maxSavedOutput
	if len(data) <= keep {
		return string(data)
	}
	return "[truncated; last " + fmt.Sprint(keep) + " bytes]\n" + string(data[len(data)-keep:])
}

func ensureImage(ctx context.Context, cell Cell, opts runOptions) error {
	_, err := dockerOutput(ctx, "image", "inspect", cell.Image)
	if err == nil && !opts.build {
		return nil
	}
	args := []string{
		"build", "-f", filepath.Join(opts.labDir, cell.Dockerfile),
		"-t", cell.Image,
		"--build-arg", "BASE=" + cell.BaseImage,
		"--build-arg", "MOSH_VERSION=" + cell.Version,
		"--build-arg", "MOSH_SHA256=" + cell.MOSHSHA256,
		opts.labDir,
	}
	fmt.Fprintf(os.Stderr, "building %s from %s\n", cell.Image, cell.BaseImage)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// ensureStockImage builds the pinned stock mosh-client oracle image when it
// is absent. The harness directory is the existing ticket-29 parity image
// recipe; this gate does not vendor a second copy of stock mosh.
func ensureStockImage(ctx context.Context, cell Cell, opts runOptions) error {
	_, err := dockerOutput(ctx, "image", "inspect", cell.Image)
	if err == nil && !opts.build {
		return nil
	}
	harness := filepath.Join(opts.root, "internal", "mosh", "terminal", "testdata", "parity", "harness")
	fmt.Fprintf(os.Stderr, "building stock parity image %s\n", cell.Image)
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", cell.Image, harness)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startDockerCell(ctx context.Context, cell Cell, opts runOptions) error {
	stopDockerCell(cell)
	args := []string{
		"run", "-d", "--name", containerName(cell),
		"-p", fmt.Sprintf("127.0.0.1:%d:22", cell.SSHHostPort()),
		"-p", fmt.Sprintf("127.0.0.1:%d:%d/udp", cell.UDPPort, cell.UDPPort),
		"-e", "MOSH_FORCED_PORT=" + fmt.Sprint(cell.UDPPort),
		cell.Image,
	}
	_, err := dockerOutput(ctx, args...)
	return err
}

func stopDockerCell(cell Cell) {
	_ = exec.Command("docker", "rm", "-f", containerName(cell)).Run()
}

func containerName(cell Cell) string {
	name := strings.NewReplacer("/", "-", ".", "-").Replace(cell.ID)
	return "natcatty-mosh-parity-" + name
}

func saveContainerLogs(cell Cell, opts runOptions, phase string) {
	if cell.Kind != kindDocker {
		return
	}
	logs, err := dockerOutput(context.Background(), "logs", containerName(cell))
	if err != nil && len(logs) == 0 {
		return
	}
	_ = os.MkdirAll(opts.outDir, 0o755)
	path := filepath.Join(opts.outDir, reportFilename(cell.ID)+"."+phase+".log")
	_ = os.WriteFile(path, logs, 0o644)
}

func dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.Bytes(), err
}

func waitTCP(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("no endpoint after %s: %w", timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// commitEnv names the environment override for runners without git. The
// FreeBSD VM runner syncs the workspace without a .git directory, so the
// workflow passes GitHub's SHA here; -commit takes precedence over it.
const commitEnv = "NATCATTY_MOSH_PARITY_COMMIT"

// gitCommit returns the commit every report and verification is bound to. The
// explicit override wins, then the environment override, then git HEAD. An
// empty commit is an error: a report with no commit cannot be told apart from
// a stale one, so the gate never silently proceeds unbounded.
func gitCommit(root, override string) (string, error) {
	for _, candidate := range []string{override, os.Getenv(commitEnv)} {
		if commit := strings.TrimSpace(candidate); commit != "" {
			return commit, nil
		}
	}
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("cannot determine the gate commit: git rev-parse HEAD in %s failed: %w; pass -commit or set %s", root, err, commitEnv)
	}
	commit := strings.TrimSpace(string(output))
	if commit == "" {
		return "", fmt.Errorf("cannot determine the gate commit: git rev-parse HEAD in %s returned an empty value; pass -commit or set %s", root, commitEnv)
	}
	return commit, nil
}

// hostUname reports the harness's kernel name (`uname -s`), with the FreeBSD
// sysctl fallback. It is recorded in every report and must agree with the cell
// platform for native cells.
func hostUname() string {
	if output, err := exec.Command("uname", "-s").Output(); err == nil {
		if value := strings.TrimSpace(string(output)); value != "" {
			return value
		}
	}
	if output, err := exec.Command("sysctl", "-n", "kern.ostype").Output(); err == nil {
		return strings.TrimSpace(string(output))
	}
	return ""
}

// assertNativeHost fails before a native cell runs when the harness is not on
// the cell's OS. This is the pre-run half of the provenance contract; verify
// checks the recorded fields again.
func assertNativeHost(cell Cell) error {
	want := cell.ExpectedLabOS()
	if want == "" || !cell.IsNative() {
		return fmt.Errorf("cell %s is not a native cell", cell.ID)
	}
	if runtime.GOOS != want {
		return fmt.Errorf("native cell %s requires host OS %q, this host is %q; refusing to write a mislabelled report", cell.ID, want, runtime.GOOS)
	}
	if uname := hostUname(); uname != "" && !strings.EqualFold(uname, want) {
		return fmt.Errorf("native cell %s requires host OS %q, uname reports %q", cell.ID, want, uname)
	}
	return nil
}

// remotePlatform reads the lab endpoint's kernel name and machine over SSH.
// Native reports must describe the endpoint that actually ran the suites, not
// only the harness: a macos/* cell pointed at a FreeBSD or Linux -lab-addr
// must fail before any suite runs. The lab account is disposable and the
// endpoint is single-use, so host-key verification is intentionally skipped.
func remotePlatform(ctx context.Context, addr string) (string, string, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", "", fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(30 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	config := &ssh.ClientConfig{
		User:            labSSHUser,
		Auth:            []ssh.AuthMethod{ssh.Password(labSSHPassword)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		return "", "", fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}
	client := ssh.NewClient(clientConn, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("ssh session on %s: %w", addr, err)
	}
	defer session.Close()
	output, err := session.Output("uname -s; uname -m")
	if err != nil {
		return "", "", fmt.Errorf("uname on %s: %w", addr, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return "", "", fmt.Errorf("uname on %s returned %q", addr, strings.TrimSpace(string(output)))
	}
	return fields[0], fields[1], nil
}

// imagePlatform reads the container image's OS and architecture.
func imagePlatform(ctx context.Context, image string) (string, string, error) {
	output, err := dockerOutput(ctx, "image", "inspect", "--format", "{{.Os}} {{.Architecture}}", image)
	if err != nil {
		return "", "", fmt.Errorf("inspect image %s: %w", image, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		return "", "", fmt.Errorf("inspect image %s: unexpected platform %q", image, strings.TrimSpace(string(output)))
	}
	return fields[0], fields[1], nil
}

// recordImagePlatform binds a Docker or stock report to the Linux image it
// exercised. A non-Linux image is a hard failure, never a recorded mismatch.
func recordImagePlatform(ctx context.Context, cell Cell, report *CellReport) error {
	labOS, labArch, err := imagePlatform(ctx, cell.Image)
	if err != nil {
		return fmt.Errorf("cell %s: %w", cell.ID, err)
	}
	if !strings.EqualFold(labOS, hostOSLinux) {
		return fmt.Errorf("cell %s: image platform is %q, want %q", cell.ID, labOS, hostOSLinux)
	}
	report.LabOS = labOS
	report.LabArch = labArch
	return nil
}

func goBinary() (string, error) {
	if value := os.Getenv("GO"); value != "" {
		return value, nil
	}
	return exec.LookPath("go")
}

// envWith applies overrides to a copy of base, removing every earlier entry
// with the same variable name so the last value is the only one.
func envWith(base []string, overrides []string) []string {
	names := make(map[string]bool, len(overrides))
	for _, entry := range overrides {
		name, _, _ := strings.Cut(entry, "=")
		names[name] = true
	}
	env := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if names[name] {
			continue
		}
		env = append(env, entry)
	}
	return append(env, overrides...)
}
