package shellhook

import (
	"encoding/base64"
	"strings"
)

// The PowerShell hook is a session-scoped script. PowerShell loads the user's
// profile before -File runs, so the script wraps whatever prompt function the
// profile left behind instead of replacing it. Preexec uses the host's
// documented PSReadLine extension point: a global PSConsoleHostReadLine calls
// PSReadLine's own ReadLine and announces the line it returns, before the host
// executes it. Every line a user accepts passes through it, including an
// immediate duplicate and a history recall; a line added programmatically with
// [PSConsoleReadLine]::AddToHistory never does, so it cannot become a capture.
// The prompt wrapper emits postexec, the prompt handshake, and OSC 7/133.
//
// The script deliberately changes nothing the user can observe: the previous
// prompt function still runs, no keybinding is touched, and the profile's own
// AddToHistoryHandler is left installed. The user's prompt runs before any
// wrapper statement so it sees the executed command's real $? and
// $LASTEXITCODE. It installs nothing when an existing integration is active,
// when PSReadLine lacks the ReadLine overload it needs, when the launch is not
// interactive, or when a parent already exported NETCATTY_HOOK without this
// shell's functions; each refusal prints the private skip marker.
//
// A machine-policy refusal happens before the script's first statement, so the
// script cannot announce it. The launchers therefore run the same file once in
// a short-lived -NoProfile -NonInteractive child first: the script prints
// NetcattyHookValidateOK when it reaches a decision, and a refusal or startup
// error exits non-zero without the marker. Only then is the interactive child
// started; a failed pre-launch check announces the skip and opens a plain
// shell. This never depends on the failed child exiting, because -NoExit keeps
// a parse-error or unreadable-script child alive at a prompt.
//
// Interactivity is decided from the launcher marker and the console handles,
// never from [Environment]::UserInteractive: Win32-OpenSSH starts an
// interactive shell in session 0 with a non-visible window station, where
// UserInteractive reports false even though the SSH PTY is a real terminal.
// The local planner exports NETCATTY_HOOK_START=1 for the shell it starts, and
// WindowsPowerShellRemoteCommand exports it for the hook child; a PowerShell
// launched without the marker and with redirected stdin/stdout is a
// non-interactive invocation and installs nothing.
const powershellHookText = `$global:__netcatty_escape = [char]27
$global:__netcatty_bell = [char]7

# The launcher runs this file once in a short-lived -NonInteractive child
# before it starts the interactive shell. In that mode the script must not
# touch the state file or the console; it only proves that it parsed, ran, and
# reached a decision, then prints a marker the launcher matches.
$global:__netcatty_validate = $env:NETCATTY_HOOK_VALIDATE -eq '1'

function global:__netcatty_skip {
    if ($global:__netcatty_validate) {
        [Console]::Out.WriteLine('NetcattyHookValidateOK')
        return
    }
    [Console]::Write($global:__netcatty_escape + ']1337;NetcattyHookSkip;powershell' + $global:__netcatty_bell)
}

# The launcher uses this file to learn that the interactive child really
# loaded it. A pre-launch check runs this same file with the validate marker
# set; writing the state then would make a refused interactive child look
# loaded, so validation never writes it.
if (-not $global:__netcatty_validate -and -not [string]::IsNullOrEmpty($env:NETCATTY_HOOK_STATE)) {
    try { [IO.File]::WriteAllText($env:NETCATTY_HOOK_STATE, '1') } catch { }
}

try {

if ($env:NETCATTY_HOOK -eq '1') {
    if (-not (Get-Command __netcatty_prompt -CommandType Function -ErrorAction SilentlyContinue)) {
        __netcatty_skip
    }
    return
}

$__netcatty_launched = $global:__netcatty_validate -or ($env:NETCATTY_HOOK_START -eq '1')
$__netcatty_console = $false
try {
    $__netcatty_console = -not ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected)
} catch {
    $__netcatty_console = $false
}
if (-not $__netcatty_launched -and -not $__netcatty_console) {
    __netcatty_skip
    return
}

if ($env:ATUIN_SESSION -or $env:STARSHIP_SHELL -or $env:STARSHIP_SESSION_KEY -or $env:ITERM_SHELL_INTEGRATION_INSTALLED) {
    __netcatty_skip
    return
}

if ((Get-Command atuin_preexec -ErrorAction SilentlyContinue) -or (Get-Command iterm2_preexec -ErrorAction SilentlyContinue) -or (Get-Command iterm2_prompt_mark -ErrorAction SilentlyContinue)) {
    __netcatty_skip
    return
}

Import-Module PSReadLine -ErrorAction SilentlyContinue
$netcatty_readline = 'Microsoft.PowerShell.PSConsoleReadLine' -as [type]
# The PSConsoleHostReadLine wrapper below needs the three-argument ReadLine.
# An older PSReadLine does not have it; defining the wrapper anyway would
# break every accepted line, so refuse the hook and keep the Tier 2/3 path.
$netcatty_read = $null
if ($null -ne $netcatty_readline) {
    $netcatty_read = $netcatty_readline.GetMethod('ReadLine', [type[]]@([System.Management.Automation.Runspaces.Runspace], [System.Management.Automation.EngineIntrinsics], [bool]))
}
if ($null -eq $netcatty_read) {
    __netcatty_skip
    return
}

function global:__netcatty_encode([string]$text) {
    if ($null -eq $text) {
        return ''
    }
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($text)
    return [Convert]::ToBase64String($bytes).TrimEnd([char]'=').Replace('+', '-').Replace('/', '_')
}

function global:__netcatty_cwd {
    $path = $null
    try { $path = $PWD.ProviderPath } catch { }
    if ([string]::IsNullOrEmpty($path)) {
        $path = [string]$PWD.Path
    }
    return [string]$path
}

function global:__netcatty_uri_path([string]$path) {
    $uri = $path -replace '\\', '/'
    if ($uri -match '^[A-Za-z]:') {
        $uri = '/' + $uri
    }
    return $uri
}

function global:__netcatty_preexec([string]$line) {
    if ([string]::IsNullOrEmpty($line)) {
        return
    }
    $esc = $global:__netcatty_escape
    $bel = $global:__netcatty_bell
    $command = __netcatty_encode $line
    $cwd = __netcatty_encode (__netcatty_cwd)
    if ($command -and $cwd) {
        [Console]::Write($esc + ']1337;NetcattyExec;powershell;' + $cwd + ';' + $command + $bel)
    }
    [Console]::Write($esc + ']133;C' + $bel)
}

# LASTEXITCODE does not exist until the first native command. Defining it as
# $null keeps the prompt wrapper's argument list safe under Set-StrictMode
# without running a command that would reset $? before the user's prompt.
if ($null -eq (Get-Variable -Name LASTEXITCODE -Scope Global -ErrorAction SilentlyContinue)) {
    $global:LASTEXITCODE = $null
}

$global:__netcatty_last_exit = $null
$global:__netcatty_error_count = 0

# The PowerShell host calls PSConsoleHostReadLine for every line the user
# accepts and executes the string it returns. PSReadLine exports the same
# function; this global definition replaces it with one that announces the
# accepted line first and then returns it. Nothing is rebound: Enter, history
# deduplication, and the profile's own AddToHistoryHandler stay exactly as the
# user configured them, so an immediate duplicate or a history recall is
# announced just like any other accepted line. A line inserted
# programmatically with [PSConsoleReadLine]::AddToHistory never reaches this
# function and therefore never produces a capture of its own.
function global:PSConsoleHostReadLine {
    # Read $? before any other statement: it is the status of the command the
    # prompt being drawn belongs to, and ReadLine uses it for the prompt color.
    $netcatty_last_status = $?
    Microsoft.PowerShell.Core\Set-StrictMode -Off
    $netcatty_line = [Microsoft.PowerShell.PSConsoleReadLine]::ReadLine($host.Runspace, $ExecutionContext, $netcatty_last_status)
    if (-not [string]::IsNullOrEmpty($netcatty_line)) {
        __netcatty_preexec $netcatty_line
        # Remember the status at the moment the line starts. LASTEXITCODE
        # persists across cmdlets, so a failed cmdlet after a failed native
        # command would otherwise look like the native command's stale code.
        $global:__netcatty_last_exit = Get-Variable -Name LASTEXITCODE -Scope Global -ValueOnly -ErrorAction SilentlyContinue
        $global:__netcatty_error_count = $Error.Count
    }
    return $netcatty_line
}

$global:__netcatty_previous_prompt = $function:prompt
if ($null -eq $global:__netcatty_previous_prompt) {
    $global:__netcatty_previous_prompt = { "PS $($executionContext.SessionState.Path.CurrentLocation)> " }
}

function global:__netcatty_finish {
    param([bool]$netcatty_ok, $netcatty_last, [int]$netcatty_errors)
    # The user's prompt runs before any other statement of this wrapper so it
    # sees the command's real $? and LASTEXITCODE. Assigning a variable (or
    # calling Get-Variable/Console::Write) first would reset $? to $true.
    $netcatty_user_prompt = & $global:__netcatty_previous_prompt
    if ($netcatty_ok) {
        $netcatty_status = 0
    } elseif ($null -ne $netcatty_last -and $netcatty_last -ne 0 -and
              ($null -eq $global:__netcatty_last_exit -or $netcatty_last -ne $global:__netcatty_last_exit)) {
        $netcatty_status = $netcatty_last
    } elseif ($netcatty_errors -gt $global:__netcatty_error_count) {
        $netcatty_status = 1
    } elseif ($null -ne $netcatty_last -and $netcatty_last -ne 0) {
        $netcatty_status = $netcatty_last
    } else {
        $netcatty_status = 1
    }
    $esc = $global:__netcatty_escape
    $bel = $global:__netcatty_bell
    $cwd = __netcatty_cwd
    $hostName = $env:COMPUTERNAME
    if ([string]::IsNullOrEmpty($hostName)) {
        try { $hostName = [System.Net.Dns]::GetHostName() } catch { $hostName = '' }
    }
    [Console]::Write($esc + ']133;D;' + $netcatty_status + $bel)
    [Console]::Write($esc + ']1337;NetcattyHook;powershell' + $bel)
    [Console]::Write($esc + ']133;A' + $bel)
    [Console]::Write($esc + ']7;file://' + $hostName + (__netcatty_uri_path $cwd) + $bel)
    [Console]::Write($esc + ']133;B' + $bel)
    $netcatty_user_prompt
}

# The status values are passed as arguments: argument evaluation reads the
# automatic variables without resetting $? for __netcatty_finish's first
# statement, which is the user's prompt call.
function global:__netcatty_prompt {
    __netcatty_finish $? $global:LASTEXITCODE $global:Error.Count
}

function global:prompt {
    __netcatty_prompt
}

$env:NETCATTY_HOOK = '1'

# The pre-launch child has reached a working install. Say so on stdout; the
# interactive child never sees the validate marker, so this text cannot leak
# into the Session.
if ($global:__netcatty_validate) {
    [Console]::Out.WriteLine('NetcattyHookValidateOK')
}

} catch {
    # A terminating startup error must not leave the Session waiting for a
    # handshake. Announce the skip; -NoExit keeps the interactive shell.
    __netcatty_skip
}
`

// interactivePowerShellArgs rejects invocations that run a command, read a
// script or an encoded command, or explicitly disable interaction. -Login is
// interactive: it starts an interactive login shell. PowerShell options are
// case-insensitive, may be abbreviated, and may carry their value after a
// colon; a colon-attached value is not a separate argument, so the next word
// must not be consumed as one.
func interactivePowerShellArgs(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
			// A bare word is a script or command; PowerShell is not
			// interactive.
			return false
		}
		name := strings.ToLower(strings.TrimLeft(arg, "-"))
		valueAttached := false
		if base, _, found := strings.Cut(name, ":"); found {
			name, valueAttached = base, true
		}
		switch {
		case strings.HasPrefix("command", name):
			return false
		case strings.HasPrefix("encodedcommand", name):
			return false
		case strings.HasPrefix("file", name):
			return false
		case len(name) >= 3 && strings.HasPrefix("noninteractive", name):
			return false
		case strings.HasPrefix("help", name), name == "?":
			return false
		case strings.HasPrefix("version", name):
			return false
		case strings.HasPrefix("embedding", name):
			return false
		case powerShellOptionTakesValue(name):
			if !valueAttached {
				index++
			}
		}
	}
	return true
}

// powerShellOptionTakesValue reports whether an option's value is the
// following word. Without this, a value such as Bypass would look like a bare
// script operand and silently lose the hook.
func powerShellOptionTakesValue(name string) bool {
	switch {
	case strings.HasPrefix(name, "executionpolicy"), name == "ex":
		return true
	case strings.HasPrefix(name, "windowstyle"), name == "w", name == "wo", name == "win", name == "wind":
		return true
	case strings.HasPrefix(name, "workingdirectory"), name == "wd", name == "work", name == "worki":
		return true
	case strings.HasPrefix(name, "configurationname"), name == "config", name == "conf", name == "con":
		return true
	case strings.HasPrefix(name, "settingsfile"), strings.HasPrefix(name, "custompipename"), name == "settings":
		return true
	case strings.HasPrefix(name, "outputformat"), name == "of", name == "o":
		return true
	case strings.HasPrefix(name, "inputformat"), name == "if", name == "i":
		return true
	case strings.HasPrefix(name, "psconsolefile"), name == "psc":
		return true
	}
	return false
}

// powershellSkipWrite is the PowerShell statement that announces the private
// skip marker. Both launchers use it when the hook never loaded, so the tap
// releases its tentative first capture and keeps Tier 2/3.
func powershellSkipWrite() string {
	return "[Console]::Write([char]27 + ']1337;NetcattyHookSkip;powershell' + [char]7);"
}

// powershellCommand starts the local PowerShell Session. The outer process is
// a bootstrap that runs the hook child exactly like the SSH wrapper does, so a
// machine policy refusing the script falls back to a plain login shell instead
// of ending the Session. The outer runs with -NoProfile: the pre-launch check
// also runs with -NoProfile, so the interactive child is the user's shell and
// the only profile load.
func powershellCommand(path string, args []string, hookPath string) []string {
	return []string{path, "-NoProfile", "-NoLogo", "-ExecutionPolicy", "Bypass", "-Command",
		powershellBootstrap(path, args, quotePowerShell(hookPath))}
}

// powershellValidateMarker is what the hook prints on stdout when it reaches a
// decision in the short-lived pre-launch child. A refused or broken script
// exits without it.
const powershellValidateMarker = "NetcattyHookValidateOK"

// powershellValidatePrefix builds the statements that run probe (a full
// "& <shell> ... -File <hook>" invocation) with NETCATTY_HOOK_VALIDATE set, and
// store whether it exited 0 and printed the marker in $<result>. The caller
// must initialize $<result> to $false. The marker variable is always cleared,
// including on failure, so the interactive child never runs in validate mode.
func powershellValidatePrefix(probe, result string) string {
	return "$env:NETCATTY_HOOK_VALIDATE='1';" +
		"try {" +
		"$netcatty_probe=" + probe + " 2>&1 | Out-String;" +
		"$" + result + "=($LASTEXITCODE -eq 0 -and $netcatty_probe -match '" + powershellValidateMarker + "')" +
		"} catch { $" + result + "=$false } finally { $env:NETCATTY_HOOK_VALIDATE=$null };"
}

// powershellBootstrap builds the command text that first validates the hook in
// a short-lived child, then starts the interactive child. A machine policy can
// refuse -File before the script's first statement, and -NoExit then leaves
// that child alive at an ordinary prompt with no skip marker; waiting for it
// to exit would silently lose every command until the Session closes. The
// pre-launch check therefore decides, never the child's exit. The child
// command is assembled from separate argv words; splatting keeps an argument
// with spaces in one piece.
func powershellBootstrap(path string, args []string, hookPath string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quotePowerShell(arg))
	}
	shell := quotePowerShell(path)
	probe := "& " + shell + " -NoProfile -NoLogo -NonInteractive -ExecutionPolicy Bypass -File " + hookPath
	child := "& " + shell + " @netcatty_args -NoLogo -NoExit -ExecutionPolicy Bypass -File " + hookPath
	fallback := "& " + shell + " @netcatty_args -NoLogo -NoExit"
	state := "$env:NETCATTY_HOOK_STATE"
	return "$netcatty_args=@(" + strings.Join(quoted, ",") + ");" +
		"$netcatty_valid=$false;" +
		"try {" +
		powershellValidatePrefix(probe, "netcatty_valid") +
		"if (-not $netcatty_valid) {" + powershellSkipWrite() + fallback + "} else {" +
		"$env:NETCATTY_HOOK_START='1';" +
		child + ";" +
		"if (-not (Test-Path -LiteralPath " + state + ")) {" + powershellSkipWrite() + fallback + "}" +
		"}" +
		"} catch {" + powershellSkipWrite() + fallback + "}"
}

// IsWindowsOpenSSH reports whether the SSH identification names Win32-OpenSSH.
// The token must stand alone: it is followed by the version separator and a
// digit, or ends the banner. A marketing string or a POSIX OpenSSH banner is
// not a Windows peer.
func IsWindowsOpenSSH(remoteVersion string) bool {
	const token = "openssh_for_windows"
	software := strings.ToLower(strings.TrimSpace(remoteVersion))
	index := strings.Index(software, token)
	if index < 0 {
		return false
	}
	rest := software[index+len(token):]
	if rest == "" {
		return true
	}
	return len(rest) > 1 && rest[0] == '_' && rest[1] >= '0' && rest[1] <= '9'
}

// WindowsDefaultShellProbeCommand reads the shell Win32-OpenSSH starts for an
// exec request. An absent value means the OpenSSH default, cmd.exe.
func WindowsDefaultShellProbeCommand() string {
	return `reg query "HKLM\SOFTWARE\OpenSSH" /v DefaultShell`
}

// ParseWindowsDefaultShell extracts the DefaultShell path from `reg query`
// output. CRLF and a localized prefix around the value are tolerated; a
// missing value returns empty, which means cmd.exe.
func ParseWindowsDefaultShell(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		lower := strings.ToLower(line)
		if !strings.HasPrefix(lower, "defaultshell") {
			continue
		}
		marker := "reg_expand_sz"
		index := strings.Index(lower, marker)
		if index < 0 {
			marker = "reg_sz"
			index = strings.Index(lower, marker)
		}
		if index < 0 {
			continue
		}
		value := strings.TrimSpace(line[index+len(marker):])
		return strings.Trim(value, `"`)
	}
	return ""
}

// powershellShellPath reports whether a DefaultShell value names PowerShell
// and returns the path unchanged. A value carrying a line break or quote
// cannot be embedded in the remote wrapper safely.
func powershellShellPath(defaultShell string) (string, bool) {
	value := strings.TrimSpace(strings.Trim(defaultShell, `"`))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", false
	}
	base := strings.ToLower(shellName(value))
	if base == "pwsh" || base == "pwsh.exe" || base == "powershell" || base == "powershell.exe" {
		return value, true
	}
	return "", false
}

// cmdShellPath reports whether a DefaultShell value names cmd.exe and returns
// a path safe to embed in the cmd wrapper. An empty value is the Win32-OpenSSH
// default, cmd.exe.
func cmdShellPath(defaultShell string) (string, bool) {
	value := strings.TrimSpace(strings.Trim(defaultShell, `"`))
	if value == "" {
		return "cmd.exe", true
	}
	base := strings.ToLower(shellName(value))
	if base != "cmd" && base != "cmd.exe" {
		return "", false
	}
	if strings.ContainsAny(value, "\r\n\"%&|<>^!") {
		return "cmd.exe", true
	}
	return value, true
}

// quotePowerShell turns a value into one literal PowerShell string. Doubling
// the single quote is PowerShell's only escape inside a single-quoted string.
func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// WindowsPowerShellRemoteCommand is the SSH RemoteCommand for a Windows
// OpenSSH peer whose default shell is PowerShell. The outer shell parses this
// command; the wrapper writes the hook into a private temporary directory,
// validates it in a short-lived -NoProfile child, starts the same PowerShell
// executable as an interactive child with the user's profile still loading,
// and removes the directory when that child exits. The hook text travels as
// base64 so no shell quoting can corrupt it.
//
// A machine policy refusing the -File script leaves an interactive -NoExit
// child alive at a prompt without ever reaching the hook, so the wrapper does
// not depend on that child exiting: the pre-launch check (or its failure)
// decides. When validation fails it announces the private skip marker and
// starts an ordinary login shell instead. Capture then falls back to Tier 2/3.
// A temporary-directory or file-write failure takes the same fallback through
// the catch block. The finally block only removes what the try created and
// never throws.
//
// Win32-OpenSSH starts the DefaultShell as `<shell> -c "<command>"`, so
// PowerShell loads the user's profile once before this wrapper runs. The
// interactive child loads it again because that child is the user's shell;
// suppressing it with -NoProfile would remove the profile from the Session.
// The bootstrap load cannot be avoided from the command text. The pre-launch
// check uses -NoProfile, so it does not add a third load.
func WindowsPowerShellRemoteCommand(shellPath string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(powershellHookText))
	shell := quotePowerShell(shellPath)
	probe := "& " + shell +
		" -NoProfile -NoLogo -NonInteractive -ExecutionPolicy Bypass -File $netcatty_hook"
	fallback := powershellSkipWrite() + "& " + shell + " -NoLogo -NoExit"
	return "$netcatty_dir=$null;$netcatty_hook=$null;$netcatty_state=$null;$netcatty_valid=$false;" +
		"try {" +
		"$netcatty_dir=Join-Path ([IO.Path]::GetTempPath()) ('netcatty-hook-'+[guid]::NewGuid().ToString('N'));" +
		"New-Item -ItemType Directory -Path $netcatty_dir -Force -ErrorAction Stop | Out-Null;" +
		"$netcatty_hook=Join-Path $netcatty_dir 'hook.ps1';" +
		"$netcatty_state=Join-Path $netcatty_dir 'loaded';" +
		"[IO.File]::WriteAllText($netcatty_hook, [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String(" +
		quotePowerShell(encoded) + ")), (New-Object System.Text.UTF8Encoding($false)));" +
		powershellValidatePrefix(probe, "netcatty_valid") +
		"if ($netcatty_valid) {" +
		"$env:NETCATTY_HOOK_START='1';" +
		"$env:NETCATTY_HOOK_STATE=$netcatty_state;" +
		"& " + shell +
		" -NoLogo -NoExit -ExecutionPolicy Bypass -File $netcatty_hook;" +
		"if (-not (Test-Path -LiteralPath $netcatty_state)) {" + fallback + "}" +
		"} else {" + fallback + "}" +
		"} catch {" + fallback +
		"} finally {" +
		"if ($null -ne $netcatty_dir) {Remove-Item -LiteralPath $netcatty_dir -Recurse -Force -ErrorAction SilentlyContinue}" +
		"}"
}

// WindowsCmdSkipCommand is the SSH RemoteCommand for a Windows OpenSSH peer
// whose default shell is cmd.exe. The hook cannot be installed there, so the
// command announces the private skip marker and then starts the user's cmd
// shell interactively. Without the marker the tap would reserve the first
// command for a handshake that never comes.
func WindowsCmdSkipCommand(shellPath string) string {
	return "echo \x1b]1337;NetcattyHookSkip;powershell\a & " + cmdQuote(shellPath)
}

// cmdQuote quotes one cmd.exe command word. A path that needs escaping beyond
// quotes was already replaced by cmd.exe in cmdShellPath.
func cmdQuote(value string) string {
	if !strings.ContainsAny(value, " \t") {
		return value
	}
	return `"` + value + `"`
}

// posixShellPath reports whether a DefaultShell names a POSIX-compatible
// shell that understands the planner's remote wrapper. The Windows executable
// suffix is optional and the comparison ignores case, like supportedShell.
func posixShellPath(defaultShell string) bool {
	value := strings.TrimSpace(strings.Trim(defaultShell, `"`))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return false
	}
	base := strings.ToLower(strings.TrimSuffix(shellName(value), ".exe"))
	switch base {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "mksh", "ash", "busybox":
		return true
	}
	return false
}

// WindowsRemoteCaptureCommand selects the remote command for a Windows
// OpenSSH peer. hookPlanned reports that the command installs the session
// hook; a cmd.exe peer receives a skip marker instead, so the Session falls
// back to Tier 2/3. ok is false for a shell Netcatty cannot wrap, in which
// case the caller opens the plain login shell rather than sending a command
// in the wrong language.
func WindowsRemoteCaptureCommand(defaultShell string) (command string, hookPlanned, ok bool) {
	if shellPath, found := powershellShellPath(defaultShell); found {
		return WindowsPowerShellRemoteCommand(shellPath), true, true
	}
	if shellPath, found := cmdShellPath(defaultShell); found {
		return WindowsCmdSkipCommand(shellPath), false, true
	}
	if posixShellPath(defaultShell) {
		return remoteWrapperCommand(), true, true
	}
	return "", false, false
}
