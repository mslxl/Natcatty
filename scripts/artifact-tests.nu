#!/usr/bin/env nu
# Drive the app's hidden packaged-artifact test mode in a shipped Natcatty build.
#
#   nu scripts/artifact-tests.nu <artifact> [--mode launch|session]
#       [--result artifact-tests-result.json] [--ssh-port 0] [--et-port 0]
#       [--session-secrets] [--timeout 180]
#
# The artifact is the platform package, not a checkout build:
#   macOS   natcatty-<version>-macos-universal.dmg
#   Linux   natcatty-<version>-linux-<arch>.tar.gz
#   Windows natcatty-<version>-windows-amd64-portable.zip
#
# The driver finds the app binary inside the artifact, sets the environment
# contract, launches the packaged app, waits for it to exit, and validates the
# atomic result JSON. The result file and the process exit code are
# authoritative. The lab secret is read from NATCATTY_SMOKE_PASSWORD in the
# environment and is never accepted as a command-line argument.
#
# CI wiring: .github/workflows/artifact-tests.yml. See docs/packaging.md,
# "Packaged artifact tests", and docs/ci/artifact-tests.md.
#
# Run from the repository root. Exit codes:
#   0  the app exited 0 and every result check passed
#   1  a driver, launch, or check failure, including a timeout
#
# At --timeout the supervisor kills the app (TERM, then KILL), so no result is
# written; the driver reports the missing result and exits 1. The supervisor's
# own 124 is internal and is not the driver's exit code.

def find-macos-binary [mount: path] {
    let candidates = (
        glob --no-dir ($mount | path join "*.app" "Contents" "MacOS" "*")
        | where {|file| ($file | path type) == "file" }
    )
    if ($candidates | is-empty) {
        error make { msg: $"no app executable under ($mount); expected *.app/Contents/MacOS/<binary>" }
    }
    $candidates | first
}

def find-linux-binary [root: path] {
    for name in ["natcatty" "Natcatty"] {
        let direct = ($root | path join $name)
        if ($direct | path exists) { return $direct }
    }
    let candidates = (
        glob --no-dir ($root | path join "**" "*")
        | where {|file|
            let base = ($file | path basename)
            (($file | path type) == "file") and (($base == "natcatty") or ($base == "Natcatty"))
        }
    )
    if ($candidates | is-empty) {
        error make { msg: $"no natcatty executable under ($root); expected the release tarball layout" }
    }
    $candidates | first
}

def find-windows-binary [root: path] {
    let direct = ($root | path join "Natcatty.exe")
    if ($direct | path exists) { return $direct }
    let candidates = (
        glob --no-dir ($root | path join "**" "*.exe")
        | where {|file|
            ((($file | path basename | str lowercase) == "natcatty.exe") and (($file | path type) == "file"))
        }
    )
    if ($candidates | is-empty) {
        error make { msg: $"no Natcatty.exe under ($root); expected the portable zip layout" }
    }
    $candidates | first
}

# Run argv with the packaged-artifact test environment. perl, preinstalled on
# macOS and Linux, supervises the child so the deadline survives a Go app that
# ignores SIGALRM: the previous "alarm shift; exec" form was a no-op after
# exec, and a blocked app ran until the workflow job timeout. The supervisor
# forwards the app's exit code, or 124 after TERM, a one-second grace, and
# KILL. The app's own 90s watchdog covers the case where perl is absent.
def run-command [argv: list, environment: record, timeout: int] {
    if ((which perl | length) > 0) {
        let supervisor = 'my $timeout = shift; my $pid = fork(); die "fork: $!" unless defined $pid; if ($pid == 0) { exec @ARGV; exit 127; } my $timed_out = 0; local $SIG{ALRM} = sub { $timed_out = 1; kill "TERM", $pid; sleep 1; kill "KILL", $pid; }; alarm $timeout; waitpid($pid, 0); my $status = $?; alarm 0; exit 124 if $timed_out; exit 128 + ($status & 127) if ($status & 127); exit $status >> 8;'
        with-env $environment {
            run-external "perl" "-e" $supervisor ($timeout | into string) ...$argv
        } | complete
    } else {
        print --stderr "artifact tests: warning: perl is unavailable, so the driver cannot enforce its timeout"
        with-env $environment { run-external ...$argv } | complete
    }
}

def run-macos [artifact: path, environment: record, work: path, timeout: int] {
    let mount = ($work | path join "mnt")
    mkdir $mount
    let attach = (
        run-external "hdiutil" "attach" "-nobrowse" "-readonly" "-mountpoint" $mount $artifact | complete
    )
    if $attach.exit_code != 0 {
        error make { msg: $"hdiutil attach failed: ($attach.stderr)" }
    }
    let outcome = (
        try {
            let binary = (find-macos-binary $mount)
            run-command [$binary] $environment $timeout
        } catch {|err|
            run-external "hdiutil" "detach" $mount "-force" | complete | ignore
            error make { msg: $"macOS packaged app discovery or launch failed: ($err.msg)" }
        }
    )
    run-external "hdiutil" "detach" $mount "-force" | complete | ignore
    $outcome
}

def run-linux [artifact: path, environment: record, work: path, timeout: int] {
    let root = ($work | path join "extract")
    mkdir $root
    let extract = (run-external "tar" "-xzf" $artifact "-C" $root | complete)
    if $extract.exit_code != 0 {
        error make { msg: $"tar extraction failed: ($extract.stderr)" }
    }
    let binary = (find-linux-binary $root)
    let headless = ((($env.DISPLAY? | default "") == "") and (($env.WAYLAND_DISPLAY? | default "") == ""))
    if $headless and ((which xvfb-run | length) == 0) {
        error make { msg: "no DISPLAY or WAYLAND_DISPLAY and xvfb-run is not installed" }
    }
    let argv = if $headless {
        ["xvfb-run" "-a" "--server-args=-screen 0 1280x800x24" $binary]
    } else {
        [$binary]
    }
    run-command $argv $environment $timeout
}

def run-windows [artifact: path, environment: record, work: path, timeout: int] {
    let root = ($work | path join "extract")
    mkdir $root
    let extract_script = 'Expand-Archive -LiteralPath $env:NATCATTY_SMOKE_ARTIFACT -DestinationPath $env:NATCATTY_SMOKE_EXTRACT -Force'
    let extract = (
        with-env {NATCATTY_SMOKE_ARTIFACT: $artifact, NATCATTY_SMOKE_EXTRACT: $root} {
            run-external "powershell" "-NoProfile" "-NonInteractive" "-Command" $extract_script
        } | complete
    )
    if $extract.exit_code != 0 {
        error make { msg: $"Expand-Archive failed: ($extract.stderr)" }
    }
    let binary = (find-windows-binary $root)
    let stdout_path = ($work | path join "app.stdout.log")
    let stderr_path = ($work | path join "app.stderr.log")
    # A GUI executable has no usable stdout, so the app writes the result file.
    # Start-Process -Wait has no timeout; Wait-Process supplies a hard deadline.
    let run_script = '$ErrorActionPreference = "Stop"; $p = Start-Process -FilePath $env:NATCATTY_SMOKE_BINARY -PassThru -NoNewWindow -RedirectStandardOutput $env:NATCATTY_SMOKE_STDOUT -RedirectStandardError $env:NATCATTY_SMOKE_STDERR; try { Wait-Process -Id $p.Id -Timeout ([int]$env:NATCATTY_SMOKE_TIMEOUT) -ErrorAction Stop } catch { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue; exit 124 }; $p.Refresh(); exit $p.ExitCode'
    let full_environment = ($environment | merge {
        NATCATTY_SMOKE_BINARY: $binary
        NATCATTY_SMOKE_STDOUT: $stdout_path
        NATCATTY_SMOKE_STDERR: $stderr_path
        NATCATTY_SMOKE_TIMEOUT: ($timeout | into string)
    })
    let outcome = (
        with-env $full_environment {
            run-external "powershell" "-NoProfile" "-NonInteractive" "-Command" $run_script
        } | complete
    )
    let captured_stdout = if ($stdout_path | path exists) { open --raw $stdout_path } else { "" }
    let captured_stderr = if ($stderr_path | path exists) { open --raw $stderr_path } else { "" }
    {
        stdout: $captured_stdout
        stderr: ($captured_stderr + $outcome.stderr)
        exit_code: $outcome.exit_code
    }
}

# The platform the packaged app must report: runtime.GOOS/runtime.GOARCH of
# the binary the driver is about to run. Nushell reports the host arch
# differently on some platforms, so map it explicitly.
def expected-platform [] {
    let os_name = ($nu.os-info.name | str lowercase)
    let goos = match $os_name {
        "macos" => "darwin"
        "linux" => "linux"
        "windows" => "windows"
        _ => { error make { msg: $"unsupported host operating system ($os_name)" } }
    }
    let arch = ($nu.os-info.arch | str lowercase)
    let goarch = match $arch {
        "aarch64" => "arm64"
        "arm64" => "arm64"
        "x86_64" => "amd64"
        "amd64" => "amd64"
        _ => { error make { msg: $"unsupported host architecture ($arch)" } }
    }
    $"($goos)/($goarch)"
}

# Validate the result contract the driver itself relies on. A result is only
# green when it has checks, matches the requested mode and the host platform,
# and carries every check that mode is required to produce. The all-checks
# verdict below still runs; this makes an incomplete or mismatched result fail
# with a precise message instead of passing on an empty checks list.
def validate-result [result: record, mode: string, session_secrets: bool] {
    let checks = ($result.checks? | default [])
    if ($checks | is-empty) {
        error make { msg: "result has no checks" }
    }
    # The app emits one check per name. Rejecting duplicates keeps a failed
    # duplicate from hiding behind an earlier passing one.
    let names = ($checks | each {|check| $check.name? | default "" })
    if (($names | uniq | length) != ($names | length)) {
        error make { msg: "result has duplicate check names" }
    }
    let actual_mode = ($result.mode? | default "")
    if $actual_mode != $mode {
        error make { msg: $"result mode ($actual_mode) does not match requested mode ($mode)" }
    }
    let platform = ($result.platform? | default "")
    if ($platform | is-empty) {
        error make { msg: "result platform is empty" }
    }
    let expected = (expected-platform)
    if $platform != $expected {
        error make { msg: $"result platform ($platform) does not match the driver host ($expected)" }
    }
    let base = if $mode == "launch" {
        ["window_visible"]
    } else {
        ["host_key" "known_host_trust"
            "mosh_host_create" "mosh_session_open" "mosh_command" "mosh_session_close"
            "et_host_create" "et_session_open" "et_command" "et_session_close"
            "close_confirmation_disabled"]
    }
    let required = if $mode == "session" and $session_secrets {
        $base | append "session_secrets"
    } else {
        $base
    }
    for name in $required {
        let matches = ($checks | where {|check| $check.name == $name})
        if ($matches | is-empty) {
            error make { msg: $"result has no ($name) check" }
        }
        if not (($matches | first).ok) {
            error make { msg: $"result check ($name) did not pass" }
        }
    }
}

def main [
    artifact: path
    --mode (-m): string = "launch"
    --result (-r): path = "artifact-tests-result.json"
    --ssh-port: int = 0
    --et-port: int = 0
    --session-secrets
    --timeout (-t): int = 180
] {
    if not ($mode in ["launch" "session"]) {
        error make { msg: $"unknown mode ($mode); use launch or session" }
    }
    if $timeout < 1 {
        error make { msg: "--timeout must be a positive number of seconds" }
    }
    let artifact_path = ($artifact | path expand)
    if not ($artifact_path | path exists) {
        error make { msg: $"artifact not found: ($artifact_path)" }
    }
    let result_path = ($result | path expand)
    let result_dir = ($result_path | path dirname)
    if not ($result_dir | path exists) { mkdir $result_dir }

    let tmp_root = if ('TMPDIR' in $env) {
        $env.TMPDIR
    } else if ('TEMP' in $env) {
        $env.TEMP
    } else {
        "/tmp"
    }
    let work = ($tmp_root | path join $"natcatty-artifact-tests-((random uuid))")
    mkdir $work
    let profile = ($work | path join "profile")
    mkdir $profile

    let ssh_port_value = if $ssh_port > 0 {
        $ssh_port | into string
    } else {
        $env.NATCATTY_SMOKE_SSH_PORT? | default ""
    }
    let et_port_value = if $et_port > 0 {
        $et_port | into string
    } else {
        $env.NATCATTY_SMOKE_ET_PORT? | default ""
    }
    let session_secrets_value = if $session_secrets {
        "1"
    } else {
        $env.NATCATTY_SMOKE_SESSION_SECRETS? | default ""
    }
    let environment = {
        NATCATTY_SMOKE: "1"
        NATCATTY_SMOKE_MODE: $mode
        NATCATTY_SMOKE_RESULT: $result_path
        NATCATTY_SMOKE_PROFILE: $profile
        NATCATTY_SMOKE_SSH_PORT: $ssh_port_value
        NATCATTY_SMOKE_ET_PORT: $et_port_value
        NATCATTY_SMOKE_PASSWORD: ($env.NATCATTY_SMOKE_PASSWORD? | default "")
        NATCATTY_SMOKE_SSH_USER: ($env.NATCATTY_SMOKE_SSH_USER? | default "")
        NATCATTY_SMOKE_SESSION_SECRETS: $session_secrets_value
        NATCATTY_SMOKE_ALLOW_UNSUPPORTED_SESSION: ($env.NATCATTY_SMOKE_ALLOW_UNSUPPORTED_SESSION? | default "")
    }
    if $mode == "session" and (($environment.NATCATTY_SMOKE_PASSWORD) | is-empty) {
        rm -rf $work
        error make { msg: "session mode needs NATCATTY_SMOKE_PASSWORD in the environment" }
    }

    let failure = (
        try {
            let os_name = $nu.os-info.name
            let outcome = if $os_name == "macos" {
                run-macos $artifact_path $environment $work $timeout
            } else if $os_name == "linux" {
                run-linux $artifact_path $environment $work $timeout
            } else if $os_name == "windows" {
                run-windows $artifact_path $environment $work $timeout
            } else {
                error make { msg: $"unsupported host operating system ($os_name)" }
            }

            if ($outcome.stdout | is-not-empty) { print $outcome.stdout }
            if ($outcome.stderr | is-not-empty) { print --stderr $outcome.stderr }
            print $"packaged app exit code: ($outcome.exit_code)"

            if not ($result_path | path exists) {
                error make { msg: $"the app exited ($outcome.exit_code) without writing ($result_path); see the captured output above" }
            }
            let result = (open --raw $result_path | from json)
            print ($result | to json)
            validate-result $result $mode $session_secrets
            if not (($result.checks? | default []) | is-empty) {
                let failed = ($result.checks | where {|check| not $check.ok})
                if not ($failed | is-empty) {
                    let names = ($failed | each {|check| $check.name} | str join ", ")
                    error make { msg: $"artifact test checks failed: ($names)" }
                }
            }
            if $outcome.exit_code != 0 {
                error make { msg: $"the app exited ($outcome.exit_code) but every result check passed" }
            }
            print $"artifact test ($mode) passed on ($result.platform)"
            null
        } catch {|err|
            let message = if (($err | describe) == "string") { $err } else { $err.msg }
            { message: $message }
        }
    )
    rm -rf $work
    if $failure != null {
        error make { msg: $failure.message }
    }
}
