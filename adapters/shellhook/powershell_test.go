package shellhook

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
)

// powershellPath is a Windows-style executable path. The planner runs on the
// host build, so the path is only named, never executed.
const powershellPath = `C:\Program Files\PowerShell\7\pwsh.exe`

func TestPlanLocalPowerShellUsesASessionFile(t *testing.T) {
	plan, failed, dir := planWith(t, appsession.StartSpec{Kind: appsession.KindLocal},
		WithDefaultShell(func() (string, error) { return powershellPath, nil }))
	if failed || !plan.HookExpected {
		t.Fatalf("PowerShell plan = failed %t, expected %t", failed, plan.HookExpected)
	}
	hookPath := filepath.Join(dir, "hook.ps1")
	command := strings.Join(plan.Spec.Command, " ")
	for _, want := range []string{
		powershellPath, "-NoLogo", "-NoExit", "-ExecutionPolicy Bypass",
		"-File " + quotePowerShell(hookPath),
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command = %q, missing %q", command, want)
		}
	}
	// The bootstrap suppresses the profile so the interactive child is the one
	// and only profile load; the child itself carries the profile env.
	if len(plan.Spec.Command) < 2 || plan.Spec.Command[1] != "-NoProfile" {
		t.Fatalf("the bootstrap does not suppress its own profile load: %q", plan.Spec.Command)
	}
	wrapper := plan.Spec.Command[len(plan.Spec.Command)-1]
	child := "& " + quotePowerShell(powershellPath) +
		" @netcatty_args -NoLogo -NoExit -ExecutionPolicy Bypass -File"
	if !strings.Contains(wrapper, child) {
		t.Fatalf("the interactive child is missing: %q", wrapper)
	}
	if strings.Contains(child, "-NoProfile") {
		t.Fatalf("the interactive child suppressed the user's profile: %q", wrapper)
	}
	// The pre-launch check must run the hook in a separate -NoProfile child
	// before the interactive child starts, and must not splat the user's
	// arguments into that check.
	probe := "& " + quotePowerShell(powershellPath) +
		" -NoProfile -NoLogo -NonInteractive -ExecutionPolicy Bypass -File " + quotePowerShell(hookPath)
	if !strings.Contains(wrapper, probe) {
		t.Fatalf("the bootstrap has no profile-free pre-launch check: %q", wrapper)
	}
	validated := strings.Index(wrapper, "NETCATTY_HOOK_VALIDATE='1'")
	started := strings.Index(wrapper, "NETCATTY_HOOK_START='1'")
	if validated < 0 || started < 0 || validated > started {
		t.Fatalf("the interactive child starts before the hook is validated: %q", wrapper)
	}
	if !strings.Contains(wrapper, powershellValidateMarker) {
		t.Fatalf("the bootstrap does not match the validate marker: %q", wrapper)
	}
	if !containsEnv(plan.Spec, "NETCATTY_HOOK_START", "1") {
		t.Fatalf("the plan does not mark the shell it starts interactive: %+v", plan.Spec.Env)
	}
	if !containsEnv(plan.Spec, "NETCATTY_HOOK_STATE", filepath.Join(dir, "hook-loaded")) {
		t.Fatalf("the plan does not name the hook state file: %+v", plan.Spec.Env)
	}
	// A hook that never loads must fall back to a usable shell, not end the
	// Session: the bootstrap checks the state file and announces the skip.
	for _, want := range []string{
		"Test-Path -LiteralPath $env:NETCATTY_HOOK_STATE",
		"]1337;NetcattyHookSkip;powershell",
		"& " + quotePowerShell(powershellPath) + " @netcatty_args -NoLogo -NoExit",
	} {
		if !strings.Contains(wrapper, want) {
			t.Fatalf("the local bootstrap is missing %q: %q", want, wrapper)
		}
	}
	hook, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(hookPath); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("hook.ps1 mode = %o, want 600", info.Mode().Perm())
	}
	for _, want := range []string{
		"function global:PSConsoleHostReadLine",
		"$function:prompt",
		"__netcatty_previous_prompt",
		"1337;NetcattyExec;powershell",
		"1337;NetcattyHook;powershell",
		"NETCATTY_HOOK_VALIDATE",
		"NetcattyHookValidateOK",
	} {
		if !strings.Contains(string(hook), want) {
			t.Fatalf("hook.ps1 is missing %q", want)
		}
	}
}

func TestPlanLocalExplicitPowerShellKeepsItsArguments(t *testing.T) {
	cases := map[string][]string{
		"pwsh":        {"/usr/local/bin/pwsh"},
		"powershell":  {"/usr/local/bin/powershell"},
		"no logo":     {"/usr/local/bin/pwsh", "-NoLogo"},
		"policy":      {"/usr/local/bin/pwsh", "-ExecutionPolicy", "RemoteSigned"},
		"workdir":     {"/usr/local/bin/pwsh", "-WorkingDirectory", "/tmp"},
		"windowstyle": {"/usr/local/bin/pwsh", "-WindowStyle", "Hidden"},
		"no profile":  {"/usr/local/bin/pwsh", "-NoProfile"},
		// -Login starts an interactive login shell; only -NonInteractive and
		// command/script modes are refused.
		"login":       {"/usr/local/bin/pwsh", "-Login"},
		"short login": {"/usr/local/bin/pwsh", "-l"},
	}
	for name, command := range cases {
		name, command := name, command
		t.Run(name, func(t *testing.T) {
			spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: command}
			plan, failed, _ := planWith(t, spec)
			if failed || !plan.HookExpected {
				t.Fatalf("plan = failed %t, expected %t", failed, plan.HookExpected)
			}
			joined := strings.Join(plan.Spec.Command, " ")
			if len(plan.Spec.Command) < 2 || plan.Spec.Command[1] != "-NoProfile" {
				t.Fatalf("the bootstrap does not suppress its own profile load: %q", joined)
			}
			wrapper := plan.Spec.Command[len(plan.Spec.Command)-1]
			// Every user word stays one argument and reaches the child before
			// the hook's own options.
			for _, arg := range command[1:] {
				if !strings.Contains(wrapper, quotePowerShell(arg)) {
					t.Fatalf("the user's argument %q was not preserved: %q", arg, wrapper)
				}
			}
			child := "& " + quotePowerShell(command[0]) +
				" @netcatty_args -NoLogo -NoExit -ExecutionPolicy Bypass -File "
			if !strings.Contains(wrapper, child) {
				t.Fatalf("the child does not start with the user's arguments: %q", wrapper)
			}
		})
	}
}

func TestPlanLocalPowerShellSkipsNonInteractiveInvocations(t *testing.T) {
	cases := map[string][]string{
		"command":           {"/usr/local/bin/pwsh", "-Command", "echo hi"},
		"short command":     {"/usr/local/bin/pwsh", "-c", "echo hi"},
		"encoded command":   {"/usr/local/bin/pwsh", "-EncodedCommand", "ZQBjAGgAbwA="},
		"short encoded":     {"/usr/local/bin/pwsh", "-e", "ZQBjAGgAbwA="},
		"file":              {"/usr/local/bin/pwsh", "-File", "script.ps1"},
		"short file":        {"/usr/local/bin/pwsh", "-f", "script.ps1"},
		"non interactive":   {"/usr/local/bin/pwsh", "-NonInteractive"},
		"help":              {"/usr/local/bin/pwsh", "-Help"},
		"version":           {"/usr/local/bin/pwsh", "-Version", "7.4"},
		"bare script":       {"/usr/local/bin/pwsh", "script.ps1"},
		"stop parsing":      {"/usr/local/bin/pwsh", "--", "script.ps1"},
		"stdin":             {"/usr/local/bin/pwsh", "-"},
		"unsupported":       {"/usr/local/bin/dash"},
		"cmd on windows":    {`C:\Windows\System32\cmd.exe`},
		"python on windows": {`C:\Python312\python.exe`},
	}
	for name, command := range cases {
		name, command := name, command
		t.Run(name, func(t *testing.T) {
			spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: command}
			dir := t.TempDir()
			plan, failed := NewPlanner(WithTempDir(func() (string, error) {
				t.Fatal("an unsupported startup created a temporary directory")
				return dir, nil
			})).Plan(spec)
			if failed {
				t.Fatal("an unsupported startup reported a failure")
			}
			if plan.Cleanup != nil || len(plan.Spec.Env) != 0 ||
				strings.Join(plan.Spec.Command, " ") != strings.Join(command, " ") {
				t.Fatalf("the StartSpec changed: %+v", plan.Spec)
			}
		})
	}
}

// PowerShell accepts a value attached with a colon, and its options may be
// abbreviated. A colon-attached value is part of the same argument, so the
// next word must not be consumed as the option's value; the abbreviations of
// -NonInteractive are still non-interactive.
func TestInteractivePowerShellArgsHandlesColonValuesAndAbbreviations(t *testing.T) {
	cases := map[string]struct {
		args []string
		want bool
	}{
		"execution policy colon":    {[]string{"-ExecutionPolicy:Bypass"}, true},
		"working directory colon":   {[]string{"-WorkingDirectory:/tmp"}, true},
		"input format colon":        {[]string{"-InputFormat:text"}, true},
		"configuration name colon":  {[]string{"-ConfigurationName:foo"}, true},
		"window style colon":        {[]string{"-WindowStyle:Hidden"}, true},
		"no exit colon":             {[]string{"-NoExit:anything"}, true},
		"no logo":                   {[]string{"-NoLogo"}, true},
		"no profile":                {[]string{"-NoProfile"}, true},
		"attached value not eaten":  {[]string{"-ExecutionPolicy:Bypass", "Bypass"}, false},
		"colon command":             {[]string{"-Command:echo hi"}, false},
		"colon file":                {[]string{"-File:script.ps1"}, false},
		"colon encoded":             {[]string{"-EncodedCommand:AAAA"}, false},
		"short non interactive":     {[]string{"-nonin"}, false},
		"shorter non interactive":   {[]string{"-nonint"}, false},
		"full non interactive":      {[]string{"-NonInteractive"}, false},
		"colon non interactive":     {[]string{"-NonInteractive:true"}, false},
		"abbreviated help":          {[]string{"-he"}, false},
		"abbreviated version":       {[]string{"-ve"}, false},
		"login":                     {[]string{"-Login"}, true},
		"short login":               {[]string{"-l"}, true},
		"abbreviated login":         {[]string{"-lo"}, true},
		"separate execution policy": {[]string{"-ExecutionPolicy", "Bypass"}, true},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			if got := interactivePowerShellArgs(testCase.args); got != testCase.want {
				t.Fatalf("interactivePowerShellArgs(%v) = %t, want %t",
					testCase.args, got, testCase.want)
			}
		})
	}
}

func TestPlanSSHMarksTheRemoteHookForLateBinding(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindSSH, Hostname: "example.test"}
	plan, failed := NewPlanner().Plan(spec)
	if failed || !plan.HookExpected {
		t.Fatalf("plan = failed %t, expected %t", failed, plan.HookExpected)
	}
	if !plan.Spec.CaptureHookRemote {
		t.Fatal("the SSH plan did not mark the remote hook for late binding")
	}
	if !strings.HasPrefix(plan.Spec.RemoteCommand, "exec /bin/sh -c '") {
		t.Fatalf("POSIX fallback = %q", plan.Spec.RemoteCommand)
	}
}

func TestPowerShellHookContract(t *testing.T) {
	script := powershellHookText
	for _, want := range []string{
		"function global:PSConsoleHostReadLine",
		"[Microsoft.PowerShell.PSConsoleReadLine]::ReadLine($host.Runspace, $ExecutionContext, $netcatty_last_status)",
		"__netcatty_preexec $netcatty_line",
		"$global:__netcatty_previous_prompt = $function:prompt",
		"function global:prompt",
		"function global:__netcatty_finish",
		"$netcatty_user_prompt = & $global:__netcatty_previous_prompt",
		"__netcatty_finish $? $global:LASTEXITCODE $global:Error.Count",
		"NETCATTY_HOOK_STATE",
		"NETCATTY_HOOK_VALIDATE",
		"NetcattyHookValidateOK",
		"__netcatty_skip",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("the PowerShell hook is missing %q", want)
		}
	}
	// Preexec is the supported PSConsoleHostReadLine extension point, not a
	// history handler or a key binding: programmatic AddToHistory calls never
	// reach it, every accepted line (duplicates included) does, and the
	// profile's own bindings and history handler stay untouched.
	for _, unwanted := range []string{
		"Set-PSReadLineOption", "Set-PSReadLineKeyHandler",
		"Get-PSReadLineKeyHandler", "GetHistoryItems", "GetBufferState",
		"AcceptLine($key, $arg)", "HistoryNoDuplicates",
		"Register-EngineEvent", "Add-Type",
	} {
		if strings.Contains(script, unwanted) {
			t.Fatalf("the PowerShell hook still touches the user's line editor: %s", unwanted)
		}
	}
	// ReadLine's own $? capture must precede every other statement in the
	// wrapper: any assignment or call first would reset it to $true.
	readLineStart := strings.Index(script, "function global:PSConsoleHostReadLine {")
	if readLineStart < 0 {
		t.Fatal("the hook has no PSConsoleHostReadLine wrapper")
	}
	statusCapture := strings.Index(script[readLineStart:], "$netcatty_last_status = $?")
	strictOff := strings.Index(script[readLineStart:], "Set-StrictMode -Off")
	readCall := strings.Index(script[readLineStart:], "::ReadLine(")
	if statusCapture < 0 || strictOff < 0 || readCall < 0 || !(statusCapture < strictOff && strictOff < readCall) {
		t.Fatalf("wrapper order: status %d, strict %d, ReadLine %d", statusCapture, strictOff, readCall)
	}
	// The overload is checked before the wrapper is installed; an older
	// PSReadLine without it must skip rather than break every accepted line.
	if !strings.Contains(script, "GetMethod('ReadLine'") {
		t.Fatal("the hook does not check for the three-argument ReadLine overload")
	}
	// The user's prompt must be the first statement of the prompt helper: any
	// assignment or command before it resets $? to $true for the profile.
	finishStart := strings.Index(script, "function global:__netcatty_finish {")
	if finishStart < 0 {
		t.Fatal("the hook has no prompt helper")
	}
	promptCall := strings.Index(script[finishStart:], "$netcatty_user_prompt = & $global:__netcatty_previous_prompt")
	statusUse := strings.Index(script[finishStart:], "$netcatty_status")
	if promptCall < 0 || statusUse < 0 || promptCall > statusUse {
		t.Fatalf("the prompt helper runs its own commands before the user's prompt: call %d, status %d",
			promptCall, statusUse)
	}
	// The private sequence precedes the standard start mark, and the prompt
	// handshake sits between the finish mark and the prompt-start mark.
	private := strings.Index(script, "1337;NetcattyExec;powershell")
	standard := strings.Index(script, "133;C")
	if private < 0 || standard < 0 || private > standard {
		t.Fatalf("private sequence at %d, OSC 133 C at %d", private, standard)
	}
	finish := strings.Index(script, "133;D;")
	handshake := strings.Index(script, "1337;NetcattyHook;powershell")
	prompt := strings.Index(script, "133;A")
	if finish < 0 || handshake < 0 || prompt < 0 || !(finish < handshake && handshake < prompt) {
		t.Fatalf("handshake order: D %d, handshake %d, A %d", finish, handshake, prompt)
	}
	if !strings.Contains(script, "1337;NetcattyHookSkip;powershell") {
		t.Fatal("the PowerShell hook has no skip marker")
	}
	// Every refusal path and the catch call the marker function. The
	// definition is one occurrence; the inherited marker, the console check,
	// the integration variables, the integration functions, and the
	// PSReadLine/ReadLine check are the five refusals, and the catch is last.
	if count := strings.Count(script, "__netcatty_skip"); count < 7 {
		t.Fatalf("the skip function appears %d times, want every refusal path and the catch", count)
	}
	if count := strings.Count(script, "NETCATTY_HOOK"); count < 2 {
		t.Fatalf("the hook does not check an inherited NETCATTY_HOOK separately: %d", count)
	}
	for _, want := range []string{
		"ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY",
		"ITERM_SHELL_INTEGRATION_INSTALLED", "atuin_preexec", "iterm2_preexec", "iterm2_prompt_mark",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("the PowerShell hook does not guard on %q", want)
		}
	}
	if !strings.Contains(script, ".TrimEnd([char]'=').Replace('+', '-').Replace('/', '_')") {
		t.Fatal("the PowerShell hook does not emit unpadded base64url")
	}
	// Validation mode must not leak into the Session: the state file and the
	// prompt/console writes are suppressed, and the marker goes to stdout.
	if !strings.Contains(script, "if (-not $global:__netcatty_validate -and -not [string]::IsNullOrEmpty($env:NETCATTY_HOOK_STATE))") {
		t.Fatal("validation mode can write the state file")
	}
	if !strings.Contains(script, "$__netcatty_launched = $global:__netcatty_validate -or") {
		t.Fatal("validation mode depends on the console handles")
	}
}

// Win32-OpenSSH starts the login shell in session 0 with a non-visible window
// station, where [Environment]::UserInteractive is false even though the SSH
// PTY is a real terminal. A hook gated on that property would refuse to
// install in every Windows OpenSSH session, so installation must follow the
// launcher marker (and the console handles) instead. This test is the
// platform-independent guard: it fails on any host, not only on Windows.
func TestPowerShellHookNeverRefusesWindowsOpenSSHSession0(t *testing.T) {
	if strings.Contains(powershellHookText, "UserInteractive") {
		t.Fatal("the hook gates on UserInteractive, which is false in Win32-OpenSSH session 0")
	}
	for _, want := range []string{
		"NETCATTY_HOOK_START",
		"[Console]::IsInputRedirected",
		"[Console]::IsOutputRedirected",
	} {
		if !strings.Contains(powershellHookText, want) {
			t.Fatalf("the hook does not decide interactivity from %q", want)
		}
	}
}

func TestPowerShellRemoteCommandQuotesSafely(t *testing.T) {
	command := WindowsPowerShellRemoteCommand(`C:\Program Files\PowerShell\7\pwsh.exe`)
	shell := quotePowerShell(`C:\Program Files\PowerShell\7\pwsh.exe`)
	for _, want := range []string{
		"[IO.Path]::GetTempPath()",
		"New-Item -ItemType Directory",
		"[IO.File]::WriteAllText",
		"[Convert]::FromBase64String",
		"-NoLogo -NoExit -ExecutionPolicy Bypass -File",
		"Remove-Item -LiteralPath",
		"$netcatty_state=Join-Path $netcatty_dir 'loaded';",
		"Test-Path -LiteralPath $netcatty_state",
		"]1337;NetcattyHookSkip;powershell",
		shell,
		powershellValidateMarker,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("remote wrapper is missing %q", want)
		}
	}
	if strings.Contains(command, "\n") {
		t.Fatal("the remote wrapper is not one SSH command line")
	}
	if strings.Contains(command, powershellHookText) {
		t.Fatal("the hook text is quoted literally instead of carried as base64")
	}
	// The hook is validated in a short-lived -NoProfile -NonInteractive child
	// before the interactive one starts, so a policy refusal is known before
	// any Session is on the line. That child must not add a profile load.
	probe := "& " + shell + " -NoProfile -NoLogo -NonInteractive -ExecutionPolicy Bypass -File $netcatty_hook"
	probeAt := strings.Index(command, probe)
	marker := strings.Index(command, "$env:NETCATTY_HOOK_START='1';")
	state := strings.Index(command, "$env:NETCATTY_HOOK_STATE=$netcatty_state;")
	launch := strings.Index(command, "& "+shell+
		" -NoLogo -NoExit -ExecutionPolicy Bypass -File $netcatty_hook;")
	if probeAt < 0 || marker < 0 || state < 0 || launch < 0 || !(probeAt < marker && marker < launch && state < launch) {
		t.Fatalf("the wrapper order is wrong: probe %d, marker %d, state %d, launch %d",
			probeAt, marker, state, launch)
	}
	if !strings.Contains(command, "if ($netcatty_valid) {") {
		t.Fatal("the wrapper starts the interactive child without gating on validation")
	}
	// The state check and the plain-shell replacement stay inside the try, so
	// a child that never loaded the hook cannot end the SSH Session.
	fallbackAt := strings.Index(command, "if (-not (Test-Path -LiteralPath $netcatty_state)) {")
	catchAt := strings.LastIndex(command, "} catch {")
	if fallbackAt < 0 || catchAt < 0 || fallbackAt > catchAt {
		t.Fatalf("the child-exit fallback is not in the try block: check %d, catch %d", fallbackAt, catchAt)
	}
	// A temp-directory or file-write failure must announce the skip marker and
	// start a login shell instead of killing the Session. New-Item's failure
	// is non-terminating unless -ErrorAction Stop makes it catchable.
	if !strings.Contains(command, "New-Item -ItemType Directory -Path $netcatty_dir -Force -ErrorAction Stop") {
		t.Fatal("the temp-directory failure is not catchable")
	}
	if catchAt < 0 {
		t.Fatal("the wrapper has no fallback for a failed install")
	}
	fallback := "& " + shell + " -NoLogo -NoExit"
	if rest := command[catchAt:]; !strings.Contains(rest, "]1337;NetcattyHookSkip;powershell") ||
		!strings.Contains(rest, fallback) {
		t.Fatalf("the wrapper's catch does not announce a skip and open a login shell: %q", rest)
	}
	// The failed-validation branch must take the same fallback, without ever
	// waiting for the refused -NoExit child to exit.
	validBranch := strings.Index(command, "if ($netcatty_valid) {")
	invalidBranch := strings.Index(command, "} else {"+powershellSkipWrite())
	if validBranch < 0 || invalidBranch < 0 || invalidBranch < validBranch {
		t.Fatalf("the wrapper has no fallback for a failed pre-launch check: %q", command)
	}
	decoded := decodeEmbeddedScript(t, command)
	if decoded != powershellHookText {
		t.Fatal("the embedded script does not round-trip")
	}
	if got := quotePowerShell("a'b"); got != `'a''b'` {
		t.Fatalf("quotePowerShell = %q", got)
	}
}

// decodeEmbeddedScript extracts the base64 argument from the generated wrapper
// and returns the script it carries.
func decodeEmbeddedScript(t *testing.T, command string) string {
	t.Helper()
	const marker = "FromBase64String("
	index := strings.Index(command, marker)
	if index < 0 {
		t.Fatal("the wrapper has no base64 payload")
	}
	rest := command[index+len(marker):]
	rest = strings.TrimPrefix(rest, "'")
	end := strings.Index(rest, "')")
	if end < 0 {
		t.Fatal("the base64 payload is not closed")
	}
	decoded, err := base64.StdEncoding.DecodeString(rest[:end])
	if err != nil {
		t.Fatalf("the base64 payload does not decode: %v", err)
	}
	return string(decoded)
}

func TestWindowsRemoteCaptureSelectsTheShellWrapper(t *testing.T) {
	cases := []struct {
		name        string
		defaultPath string
		// kind is "powershell" or "cmd"; wantShell is the path the wrapper
		// starts.
		kind      string
		wantShell string
		wantHook  bool
		ok        bool
	}{
		{name: "pwsh", defaultPath: `C:\Program Files\PowerShell\7\pwsh.exe`, kind: "powershell", wantShell: `C:\Program Files\PowerShell\7\pwsh.exe`, wantHook: true, ok: true},
		{name: "powershell", defaultPath: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, kind: "powershell", wantShell: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, wantHook: true, ok: true},
		{name: "quoted", defaultPath: `"C:\Program Files\PowerShell\7\pwsh.exe"`, kind: "powershell", wantShell: `C:\Program Files\PowerShell\7\pwsh.exe`, wantHook: true, ok: true},
		{name: "cmd", defaultPath: `C:\Windows\System32\cmd.exe`, kind: "cmd", wantShell: `C:\Windows\System32\cmd.exe`, ok: true},
		{name: "unset is cmd", defaultPath: "", kind: "cmd", wantShell: "cmd.exe", ok: true},
		{name: "cmd metacharacters", defaultPath: `C:\odd!path\cmd.exe`, kind: "cmd", wantShell: "cmd.exe", ok: true},
		{name: "git bash", defaultPath: `C:\Program Files\Git\bin\bash.exe`, kind: "posix", wantHook: true, ok: true},
		{name: "wsl", defaultPath: `C:\Windows\System32\wsl.exe`, ok: false},
		{name: "python", defaultPath: `C:\Python312\python.exe`, ok: false},
	}
	for _, test := range cases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got, hookPlanned, ok := WindowsRemoteCaptureCommand(test.defaultPath)
			if ok != test.ok {
				t.Fatalf("ok = %t, want %t", ok, test.ok)
			}
			if !ok {
				if got != "" {
					t.Fatalf("an unsupported shell returned %d bytes", len(got))
				}
				return
			}
			if hookPlanned != test.wantHook {
				t.Fatalf("hookPlanned = %t, want %t", hookPlanned, test.wantHook)
			}
			want := ""
			switch test.kind {
			case "powershell":
				want = WindowsPowerShellRemoteCommand(test.wantShell)
			case "cmd":
				want = WindowsCmdSkipCommand(test.wantShell)
			case "posix":
				want = remoteWrapperCommand()
			}
			if got != want {
				t.Fatalf("%s wrapper mismatch", test.kind)
			}
		})
	}
}

func TestWindowsCmdSkipCommandAnnouncesTheSkip(t *testing.T) {
	command := WindowsCmdSkipCommand(`C:\Windows\System32\cmd.exe`)
	if !strings.Contains(command, "\x1b]1337;NetcattyHookSkip;powershell\a") {
		t.Fatalf("the skip marker is missing: %q", command)
	}
	if !strings.HasSuffix(command, `C:\Windows\System32\cmd.exe`) {
		t.Fatalf("the wrapper does not start the user's cmd: %q", command)
	}
	spaced := WindowsCmdSkipCommand(`C:\Program Files\cmd.exe`)
	if !strings.HasSuffix(spaced, `"C:\Program Files\cmd.exe"`) {
		t.Fatalf("a path with a space was not quoted: %q", spaced)
	}
	if strings.Contains(command, "1337;NetcattyHook;") {
		t.Fatal("the cmd wrapper announces a handshake it cannot provide")
	}
}

func TestIsWindowsOpenSSH(t *testing.T) {
	positives := []string{
		"SSH-2.0-OpenSSH_for_Windows_9.5",
		"SSH-2.0-OpenSSH_for_Windows_8.9p1",
		"ssh-2.0-openssh_for_windows_9.5",
	}
	for _, value := range positives {
		if !IsWindowsOpenSSH(value) {
			t.Fatalf("%q was not recognized as Windows OpenSSH", value)
		}
	}
	negatives := []string{
		"", "SSH-2.0-OpenSSH_9.6", "SSH-2.0-dropbear_2022",
		"SSH-2.0-OpenSSH_for_WindowsNotTheToken",
	}
	for _, value := range negatives {
		if IsWindowsOpenSSH(value) {
			t.Fatalf("%q was mistaken for Windows OpenSSH", value)
		}
	}
}

func TestParseWindowsDefaultShell(t *testing.T) {
	cases := map[string]string{
		"powershell": "HKEY_LOCAL_MACHINE\\SOFTWARE\\OpenSSH\r\n    DefaultShell    REG_SZ    C:\\Program Files\\PowerShell\\7\\pwsh.exe\r\n",
		"quoted":     "    DefaultShell    REG_SZ    \"C:\\Program Files\\PowerShell\\7\\pwsh.exe\"",
		"spaces":     "    DefaultShell    REG_SZ    C:\\Program Files\\PowerShell\\7\\pwsh.exe",
		"missing":    "ERROR: The system was unable to find the specified registry key or value.",
		"empty":      "",
		"other":      "    DefaultShell    REG_EXPAND_SZ    %TEMP%\\shell.exe",
	}
	wants := map[string]string{
		"powershell": `C:\Program Files\PowerShell\7\pwsh.exe`,
		"quoted":     `C:\Program Files\PowerShell\7\pwsh.exe`,
		"spaces":     `C:\Program Files\PowerShell\7\pwsh.exe`,
		"missing":    "",
		"empty":      "",
		"other":      `%TEMP%\shell.exe`,
	}
	for name, output := range cases {
		if got := ParseWindowsDefaultShell(output); got != wants[name] {
			t.Fatalf("%s: ParseWindowsDefaultShell = %q, want %q", name, got, wants[name])
		}
	}
}

// A generated remote wrapper must never let a DefaultShell value escape its
// PowerShell string.
func TestWindowsRemoteCommandEscapesSingleQuotes(t *testing.T) {
	command := WindowsPowerShellRemoteCommand(`C:\odd'path\pwsh.exe`)
	if !strings.Contains(command, `'C:\odd''path\pwsh.exe'`) {
		t.Fatalf("the single quote was not doubled: %q", command)
	}
}
