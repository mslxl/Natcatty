package shellhook

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
)

// TestPlanOnBSDPOSIXHostsInstallsTheHook proves the existing POSIX startup
// mechanism is not Linux-specific: the same session-scoped files work on the
// BSDs, so a BSD POSIX Session reaches Tier 1. A BSD default shell Netcatty
// does not support stays unchanged and falls back to Tier 2/3, and Windows
// never receives a hook.
func TestPlanOnBSDPOSIXHostsInstallsTheHook(t *testing.T) {
	for _, goos := range []string{"freebsd", "openbsd", "netbsd", "dragonfly", "darwin", "linux"} {
		goos := goos
		for _, command := range [][]string{
			{"/bin/zsh"},
			{"/usr/local/bin/bash"},
			{"/usr/local/bin/fish"},
		} {
			command := command
			t.Run(goos+"/"+filepath.Base(command[0]), func(t *testing.T) {
				spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: command}
				plan, failed, _ := planWith(t, spec, WithPlatform(goos))
				if failed || !plan.HookExpected {
					t.Fatalf("plan = failed %t, expected %t", failed, plan.HookExpected)
				}
			})
		}
	}

	t.Run("bsd unsupported default shell", func(t *testing.T) {
		spec := appsession.StartSpec{Kind: appsession.KindLocal}
		dir := t.TempDir()
		plan, failed := NewPlanner(
			WithPlatform("freebsd"),
			WithDefaultShell(func() (string, error) { return "/bin/csh", nil }),
			WithTempDir(func() (string, error) {
				t.Fatal("an unsupported BSD shell created a temporary directory")
				return dir, nil
			}),
		).Plan(spec)
		if failed || plan.HookExpected || plan.Cleanup != nil {
			t.Fatalf("unsupported BSD plan = %+v, failed %t", plan, failed)
		}
	})

	t.Run("windows powershell", func(t *testing.T) {
		// Windows is supported through the PowerShell hook; the POSIX matrix
		// above covers only the shells the platform does not own.
		spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: []string{"C:/Program Files/PowerShell/pwsh.exe"}}
		plan, failed := NewPlanner(WithPlatform("windows")).Plan(spec)
		if failed || !plan.HookExpected || plan.Cleanup == nil {
			t.Fatalf("windows powershell plan = %+v, failed %t", plan, failed)
		}
		plan.Cleanup()
	})

	t.Run("windows posix shell stays unchanged", func(t *testing.T) {
		spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: []string{"C:/Program Files/Git/bin/bash.exe"}}
		plan, failed := NewPlanner(WithPlatform("windows")).Plan(spec)
		if failed || plan.HookExpected || plan.Cleanup != nil {
			t.Fatalf("windows POSIX plan = %+v, failed %t", plan, failed)
		}
	})
}

// TestPlanLocalNushellInstallsTheHook proves Nushell reaches Tier 1 through its
// pre_execution and pre_prompt hooks. The hook is appended to the user hooks,
// not substituted for them, and it runs after the user config through -e.
func TestPlanLocalNushellInstallsTheHook(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindLocal}
	plan, failed, dir := planWith(t, spec, WithDefaultShell(func() (string, error) {
		return "/opt/homebrew/bin/nu", nil
	}))
	if failed || !plan.HookExpected {
		t.Fatalf("nushell plan = failed %t, expected %t", failed, plan.HookExpected)
	}
	command := plan.Spec.Command
	if len(command) != 4 || command[0] != "/opt/homebrew/bin/nu" || command[1] != "-l" ||
		command[2] != "-e" || command[3] != `source "`+filepath.Join(dir, "hook.nu")+`"` {
		t.Fatalf("command = %v", command)
	}
	hook, err := os.ReadFile(filepath.Join(dir, "hook.nu"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(hook)
	for _, want := range []string{
		"pre_execution", "pre_prompt", "commandline", "1337;NetcattyExec;nu",
		"1337;NetcattyHook;nu", "encode base64 --url --nopad", "NETCATTY_HOOK",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("nushell hook is missing %q", want)
		}
	}
	plan.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %s behind: %v", dir, err)
	}
}

func TestPlanLocalExplicitNushellKeepsItsArguments(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: []string{"/bin/nu", "-i"}}
	plan, failed, dir := planWith(t, spec)
	if failed || !plan.HookExpected {
		t.Fatalf("explicit nushell plan = failed %t, expected %t", failed, plan.HookExpected)
	}
	command := plan.Spec.Command
	if len(command) != 4 || command[0] != "/bin/nu" || command[1] != "-i" ||
		command[2] != "-e" {
		t.Fatalf("command = %v, want the user's -i before the hook -e", command)
	}
	if !strings.Contains(command[3], filepath.Join(dir, "hook.nu")) {
		t.Fatalf("command = %v", command)
	}
}

// TestPlanLocalXonshInstallsTheHook proves xonsh reaches Tier 1 through its
// on_precommand and on_postcommand events. The hook directory is appended to
// the user's rc.d directories, so all user startup files still run first.
func TestPlanLocalXonshInstallsTheHook(t *testing.T) {
	spec := appsession.StartSpec{
		Kind: appsession.KindLocal,
		Env:  []string{"HOME=/session/home", "XDG_CONFIG_HOME=/session/config"},
	}
	plan, failed, dir := planWith(t, spec, WithDefaultShell(func() (string, error) {
		return "/usr/local/bin/xonsh", nil
	}))
	if failed || !plan.HookExpected {
		t.Fatalf("xonsh plan = failed %t, expected %t", failed, plan.HookExpected)
	}
	if len(plan.Spec.Command) != 0 {
		t.Fatalf("the default xonsh command was changed: %v", plan.Spec.Command)
	}
	wantRC := "/etc/xonsh/rc.d:/session/config/xonsh/rc.d:" + dir
	if !containsEnv(plan.Spec, "XONSHRC_DIR", wantRC) {
		t.Fatalf("XONSHRC_DIR was not appended: %v", plan.Spec.Env)
	}
	hook, err := os.ReadFile(filepath.Join(dir, "hook.xsh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(hook)
	for _, want := range []string{
		"on_precommand", "on_postcommand", "1337;NetcattyExec;xonsh",
		"1337;NetcattyHook;xonsh", "XONSH_INTERACTIVE", "NETCATTY_HOOK",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("xonsh hook is missing %q", want)
		}
	}
}

func TestPlanLocalXonshAppendsToAnExplicitRCDir(t *testing.T) {
	spec := appsession.StartSpec{
		Kind: appsession.KindLocal,
		Env:  []string{"XONSHRC_DIR=/srv/xonsh/rc.d"},
	}
	plan, failed, dir := planWith(t, spec, WithDefaultShell(func() (string, error) {
		return "/usr/bin/xonsh", nil
	}))
	if failed {
		t.Fatal("the xonsh plan reported a failure")
	}
	if !containsEnv(plan.Spec, "XONSHRC_DIR", "/srv/xonsh/rc.d:"+dir) {
		t.Fatalf("the explicit XONSHRC_DIR was not preserved: %v", plan.Spec.Env)
	}
}

func TestPlanLocalSkipsNonInteractiveNushellAndXonsh(t *testing.T) {
	cases := map[string][]string{
		"nu command":             {"/bin/nu", "-c", "echo hi"},
		"nu execute":             {"/bin/nu", "-e", "echo hi"},
		"nu commands attached":   {"/bin/nu", "--commands=echo hi"},
		"nu execute attached":    {"/bin/nu", "--execute=echo hi"},
		"nu command short":       {"/bin/nu", "-cecho hi"},
		"nu no config":           {"/bin/nu", "--no-config-file"},
		"nu script":              {"/bin/nu", "script.nu"},
		"nu stdin":               {"/bin/nu", "--stdin"},
		"nu mcp":                 {"/bin/nu", "--mcp"},
		"xonsh command":          {"/bin/xonsh", "-c", "echo hi"},
		"xonsh command attached": {"/bin/xonsh", "--command=print(1)"},
		"xonsh no rc":            {"/bin/xonsh", "--no-rc"},
		"xonsh short no rc":      {"/bin/xonsh", "-n"},
		"xonsh rc":               {"/bin/xonsh", "--rc", "/tmp/user.xsh"},
		"xonsh rc attached":      {"/bin/xonsh", "--rc=/tmp/user.xsh"},
		"xonsh script":           {"/bin/xonsh", "script.xsh"},
		"xonsh version":          {"/bin/xonsh", "-V"},
	}
	for name, command := range cases {
		command := command
		t.Run(name, func(t *testing.T) {
			spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: command}
			dir := t.TempDir()
			plan, failed := NewPlanner(WithTempDir(func() (string, error) {
				t.Fatal("an unsupported startup created a temporary directory")
				return dir, nil
			})).Plan(spec)
			if failed || plan.HookExpected || plan.Cleanup != nil {
				t.Fatalf("plan = %+v, failed %t", plan, failed)
			}
			if strings.Join(plan.Spec.Command, " ") != strings.Join(command, " ") {
				t.Fatalf("the StartSpec changed: %v", plan.Spec.Command)
			}
		})
	}
}

// planWith runs the planner with a real session-scoped temporary directory and
// returns the plan plus the directory for assertions.
func planWith(t *testing.T, spec appsession.StartSpec, opts ...Option) (appsession.CaptureHookPlan, bool, string) {
	t.Helper()
	dir := t.TempDir()
	created := false
	opts = append(opts, WithTempDir(func() (string, error) {
		created = true
		return dir, nil
	}))
	plan, failed := NewPlanner(opts...).Plan(spec)
	if !created {
		t.Fatal("the planner did not create a session directory")
	}
	return plan, failed, dir
}

func containsEnv(spec appsession.StartSpec, key, value string) bool {
	for _, entry := range spec.Env {
		if entry == key+"="+value {
			return true
		}
	}
	return false
}

// The plan tells the Session whether a hook was actually injected. That flag
// keeps a command typed before the first handshake tentative rather than
// finalizing it at the shell's pre-hook prompt.
func TestPlanReportsWhetherTheHookIsExpected(t *testing.T) {
	t.Run("local zsh", func(t *testing.T) {
		plan, failed, _ := planWith(t, appsession.StartSpec{Kind: appsession.KindLocal},
			WithDefaultShell(func() (string, error) { return "/bin/zsh", nil }))
		if failed || !plan.HookExpected {
			t.Fatalf("zsh plan = failed %t, expected %t", failed, plan.HookExpected)
		}
	})
	t.Run("local bash", func(t *testing.T) {
		plan, failed, _ := planWith(t, appsession.StartSpec{
			Kind: appsession.KindLocal, Command: []string{"/bin/bash"},
		})
		if failed || !plan.HookExpected {
			t.Fatalf("bash plan = failed %t, expected %t", failed, plan.HookExpected)
		}
	})
	t.Run("local fish", func(t *testing.T) {
		plan, failed, _ := planWith(t, appsession.StartSpec{
			Kind: appsession.KindLocal, Command: []string{"/usr/bin/fish"},
		})
		if failed || !plan.HookExpected {
			t.Fatalf("fish plan = failed %t, expected %t", failed, plan.HookExpected)
		}
	})
	t.Run("ssh wrapper", func(t *testing.T) {
		plan, failed := NewPlanner().Plan(appsession.StartSpec{
			Kind: appsession.KindSSH, Hostname: "example.test",
		})
		if failed || !plan.HookExpected || plan.Spec.RemoteCommand == "" {
			t.Fatalf("ssh plan = failed %t, expected %t, remote %q",
				failed, plan.HookExpected, plan.Spec.RemoteCommand)
		}
	})
	t.Run("unsupported shell", func(t *testing.T) {
		plan, failed := NewPlanner().Plan(appsession.StartSpec{
			Kind: appsession.KindLocal, Command: []string{"/bin/ksh"},
		})
		if failed || plan.HookExpected {
			t.Fatalf("unsupported plan = failed %t, expected %t", failed, plan.HookExpected)
		}
	})
	t.Run("user remote command", func(t *testing.T) {
		plan, failed := NewPlanner().Plan(appsession.StartSpec{
			Kind: appsession.KindSSH, Hostname: "example.test", RemoteCommand: "uptime",
		})
		if failed || plan.HookExpected || plan.Spec.RemoteCommand != "uptime" {
			t.Fatalf("user command plan = failed %t, expected %t, remote %q",
				failed, plan.HookExpected, plan.Spec.RemoteCommand)
		}
	})
	t.Run("failed install", func(t *testing.T) {
		plan, failed := NewPlanner(
			WithDefaultShell(func() (string, error) { return "/bin/zsh", nil }),
			WithTempDir(func() (string, error) { return "", errors.New("no directory") }),
		).Plan(appsession.StartSpec{Kind: appsession.KindLocal})
		if !failed || plan.HookExpected {
			t.Fatalf("failed plan = failed %t, expected %t", failed, plan.HookExpected)
		}
	})
}

func TestPlanLeavesUnsupportedTransportsAndStartupsAlone(t *testing.T) {
	shellPlanner := func() []Option {
		return []Option{WithDefaultShell(func() (string, error) { return "/bin/zsh", nil })}
	}
	unchanged := func(t *testing.T, plan appsession.CaptureHookPlan, spec appsession.StartSpec, failed bool) {
		t.Helper()
		if failed {
			t.Fatal("an unsupported transport reported a failure")
		}
		if plan.Cleanup != nil {
			t.Fatal("an unsupported transport received a cleanup")
		}
		if plan.Spec.RemoteCommand != spec.RemoteCommand || len(plan.Spec.Command) != len(spec.Command) ||
			len(plan.Spec.Env) != len(spec.Env) {
			t.Fatalf("the StartSpec was rewritten: %+v", plan.Spec)
		}
	}

	t.Run("disabled", func(t *testing.T) {
		spec := appsession.StartSpec{Kind: appsession.KindLocal}
		plan, failed := NewPlanner(append(shellPlanner(), WithEnabled(func() bool { return false }))...).Plan(spec)
		unchanged(t, plan, spec, failed)
	})
	for _, kind := range []appsession.Kind{
		appsession.KindET, appsession.KindTelnet, appsession.KindSerial, appsession.KindMosh,
	} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			spec := appsession.StartSpec{Kind: kind}
			dir := t.TempDir()
			plan, failed := NewPlanner(append(shellPlanner(), WithTempDir(func() (string, error) {
				t.Fatal("an unsupported transport created a temporary directory")
				return dir, nil
			}))...).Plan(spec)
			unchanged(t, plan, spec, failed)
		})
	}
	t.Run("ssh remote command", func(t *testing.T) {
		spec := appsession.StartSpec{Kind: appsession.KindSSH, RemoteCommand: "tmux attach"}
		plan, failed := NewPlanner(shellPlanner()...).Plan(spec)
		unchanged(t, plan, spec, failed)
	})
	t.Run("ssh tmux attach", func(t *testing.T) {
		spec := appsession.StartSpec{Kind: appsession.KindSSH, AutoAttachTmux: true}
		plan, failed := NewPlanner(shellPlanner()...).Plan(spec)
		unchanged(t, plan, spec, failed)
	})
	t.Run("ssh network device", func(t *testing.T) {
		spec := appsession.StartSpec{Kind: appsession.KindSSH, NetworkDevice: true}
		plan, failed := NewPlanner(shellPlanner()...).Plan(spec)
		unchanged(t, plan, spec, failed)
	})
}

func TestPlanSSHInstallsTheRemoteWrapper(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindSSH, Hostname: "example.test"}
	plan, failed := NewPlanner().Plan(spec)
	if failed {
		t.Fatal("the SSH plan reported a failure")
	}
	if !strings.HasPrefix(plan.Spec.RemoteCommand, "exec /bin/sh -c '") {
		t.Fatalf("remote command = %q", plan.Spec.RemoteCommand)
	}
	if !plan.Spec.CaptureHookRemote {
		t.Fatal("the SSH plan did not mark RemoteCommand as the capture wrapper")
	}
	if plan.Cleanup != nil {
		t.Fatal("the remote wrapper cleans up remotely, not locally")
	}
	if plan.Spec.Command != nil || len(plan.Spec.Env) != 0 {
		t.Fatalf("the SSH plan changed local fields: %+v", plan.Spec)
	}
}

// Only the shells whose command language accepts a POSIX command and that
// have a hook keep the wrapper. Every other login shell falls back to the
// plain login shell before sshd can hand it a script it cannot parse.
func TestRemoteLoginShellSupportUsesOnlyThePOSIXHookShells(t *testing.T) {
	for _, shell := range []string{
		"/bin/zsh", "/bin/bash", "/usr/local/bin/fish", "-zsh",
	} {
		if !RemoteLoginShellSupported(shell) {
			t.Errorf("RemoteLoginShellSupported(%q) = false, want true", shell)
		}
		if got := RemoteCommandForLoginShell("wrapper", shell); got != "wrapper" {
			t.Errorf("RemoteCommandForLoginShell(%q) = %q, want the wrapper", shell, got)
		}
	}
	for _, shell := range []string{
		"/usr/local/bin/xonsh", "/bin/csh", "/bin/tcsh", "/opt/homebrew/bin/nu",
		"/bin/ksh", "/bin/sh", "/bin/dash", "", "/usr/local/bin/myshell",
	} {
		if RemoteLoginShellSupported(shell) {
			t.Errorf("RemoteLoginShellSupported(%q) = true, want false", shell)
		}
		if got := RemoteCommandForLoginShell("wrapper", shell); got != "" {
			t.Errorf("RemoteCommandForLoginShell(%q) = %q, want the plain login shell", shell, got)
		}
	}
}

func TestPlanLocalZshUsesASessionZDOTDIR(t *testing.T) {
	t.Setenv("ZDOTDIR", "/home/example/.config/zsh")
	spec := appsession.StartSpec{Kind: appsession.KindLocal}
	plan, failed, dir := planWith(t, spec, WithDefaultShell(func() (string, error) {
		return "/bin/zsh", nil
	}))
	if failed {
		t.Fatal("the zsh plan reported a failure")
	}
	if len(plan.Spec.Command) != 0 {
		t.Fatalf("the default zsh command was changed: %v", plan.Spec.Command)
	}
	if !containsEnv(plan.Spec, "ZDOTDIR", dir) {
		t.Fatalf("ZDOTDIR was not redirected: %v", plan.Spec.Env)
	}
	if !containsEnv(plan.Spec, "NETCATTY_HOOK_DIR", dir) ||
		!containsEnv(plan.Spec, "NETCATTY_USER_ZDOTDIR", "/home/example/.config/zsh") {
		t.Fatalf("hook environment = %v", plan.Spec.Env)
	}
	for _, name := range []string{".zshenv", ".zprofile", ".zshrc", ".zlogin", ".zlogout"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("startup file %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
	rc, err := os.ReadFile(filepath.Join(dir, ".zshrc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rc), `source "$ZDOTDIR/.zshrc"`) {
		t.Fatal("the user's .zshrc is not sourced")
	}
	plan.Cleanup()
	plan.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup left %s behind: %v", dir, err)
	}
}

func TestPlanLocalZshHonorsAnExplicitZDOTDIR(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindLocal, Env: []string{"ZDOTDIR=/srv/zdot"}}
	plan, failed, _ := planWith(t, spec, WithDefaultShell(func() (string, error) {
		return "/usr/local/bin/zsh", nil
	}))
	if failed {
		t.Fatal("the zsh plan reported a failure")
	}
	if !containsEnv(plan.Spec, "NETCATTY_USER_ZDOTDIR", "/srv/zdot") {
		t.Fatalf("the explicit ZDOTDIR was lost: %v", plan.Spec.Env)
	}
}

func TestPlanLocalBashReplacesLoginWithTheHookRC(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: []string{"/bin/bash", "-l"}}
	plan, failed, dir := planWith(t, spec)
	if failed {
		t.Fatal("the bash plan reported a failure")
	}
	command := plan.Spec.Command
	if len(command) < 4 || command[0] != "/bin/bash" {
		t.Fatalf("command = %v", command)
	}
	for _, arg := range command {
		if arg == "-l" || arg == "--login" {
			t.Fatalf("a login bash ignores --rcfile: %v", command)
		}
	}
	rcPath := filepath.Join(dir, "bashrc")
	if !strings.Contains(strings.Join(command, " "), "--rcfile "+rcPath+" -i") {
		t.Fatalf("command = %v", command)
	}
	rc, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(rc)
	systemProfile := strings.Index(body, ". /etc/profile")
	profile := strings.Index(body, `if [ -f "$__netcatty_user_home/.bash_profile" ]`)
	shellRC := strings.Index(body, `[ "$__netcatty_bashrc_sourced" = "0" ]`)
	hook := strings.Index(body, "NETCATTY_HOOK")
	if systemProfile < 0 || profile < 0 || shellRC < 0 || hook < 0 ||
		!(systemProfile < profile && profile < shellRC && shellRC < hook) {
		t.Fatalf("startup order is wrong (etc %d, profile %d, rc %d, hook %d)",
			systemProfile, profile, shellRC, hook)
	}
	if count := strings.Count(body, `. "$__netcatty_user_home/.bashrc"`); count != 1 {
		t.Fatalf(".bashrc is sourced %d times, want once", count)
	}
	if !strings.Contains(body, "__netcatty_bashrc_sourced=1") {
		t.Fatal("the profile's own .bashrc source is not detected")
	}
	if info, err := os.Stat(rcPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("bashrc mode = %v, err = %v", info, err)
	}
}

func TestPlanLocalFishAppendsAnInitCommand(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindLocal}
	plan, failed, dir := planWith(t, spec, WithDefaultShell(func() (string, error) {
		return "/opt/homebrew/bin/fish", nil
	}))
	if failed {
		t.Fatal("the fish plan reported a failure")
	}
	command := strings.Join(plan.Spec.Command, " ")
	if !strings.HasPrefix(command, "/opt/homebrew/bin/fish -l --init-command ") {
		t.Fatalf("command = %q", command)
	}
	if !strings.Contains(command, `source "$NETCATTY_HOOK_FILE"`) {
		t.Fatalf("command = %q", command)
	}
	if !containsEnv(plan.Spec, "NETCATTY_HOOK_FILE", filepath.Join(dir, "hook.fish")) {
		t.Fatalf("hook environment = %v", plan.Spec.Env)
	}
	hook, err := os.ReadFile(filepath.Join(dir, "hook.fish"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hook), "fish_preexec") {
		t.Fatal("the fish hook does not register a preexec handler")
	}
}

func TestPlanLocalExplicitFishKeepsItsLoginChoice(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: []string{"/bin/fish"}}
	plan, failed, _ := planWith(t, spec)
	if failed {
		t.Fatal("the fish plan reported a failure")
	}
	command := strings.Join(plan.Spec.Command, " ")
	if strings.HasPrefix(command, "/bin/fish -l ") {
		t.Fatalf("the explicit login choice was overridden: %q", command)
	}
	if !strings.Contains(command, "--init-command") {
		t.Fatalf("command = %q", command)
	}
}

// fish runs every --init-command in order, so an explicit one is preserved and
// the hook is appended after it.
func TestPlanLocalExplicitFishKeepsItsInitCommand(t *testing.T) {
	spec := appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: []string{"/bin/fish", "-C", "set -g user_init 1"},
	}
	plan, failed, _ := planWith(t, spec)
	if failed {
		t.Fatal("the fish plan reported a failure")
	}
	command := strings.Join(plan.Spec.Command, " ")
	user := strings.Index(command, "set -g user_init 1")
	hook := strings.Index(command, "NETCATTY_HOOK_FILE")
	if user < 0 || hook < 0 || user > hook {
		t.Fatalf("the user init command was not preserved before the hook: %q", command)
	}
}

// An option value is not a script operand. zsh -o and bash -O name a shell
// option and leave the shell interactive, so the hook must still be planned
// while the user's arguments are preserved in the command.
func TestPlanLocalAcceptsOptionsWithValues(t *testing.T) {
	cases := map[string][]string{
		"zsh option":   {"/bin/zsh", "-o", "extendedglob"},
		"zsh cluster":  {"/bin/zsh", "-xo", "extendedglob"},
		"bash shopt":   {"/bin/bash", "-O", "extglob"},
		"bash cluster": {"/bin/bash", "-xO", "extglob"},
	}
	for name, command := range cases {
		name, command := name, command
		t.Run(name, func(t *testing.T) {
			spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: command}
			plan, failed, _ := planWith(t, spec)
			if failed {
				t.Fatal("the plan reported a failure")
			}
			if plan.Cleanup == nil {
				t.Fatalf("the hook was not planned: %+v", plan.Spec)
			}
			joined := strings.Join(plan.Spec.Command, " ")
			if !strings.Contains(joined, strings.Join(command[1:], " ")) {
				t.Fatalf("the user's options were lost: %q", joined)
			}
		})
	}
}

// zdotdirFor resolves the session's HOME, not the process HOME: the local
// transport starts the shell with StartSpec.Env, so the user's zsh startup
// files live under that home when the caller overrides it.
func TestPlanLocalZshUsesTheSessionHome(t *testing.T) {
	t.Setenv("ZDOTDIR", "")
	t.Setenv("HOME", "/process/home")
	spec := appsession.StartSpec{
		Kind: appsession.KindLocal,
		Env:  []string{"HOME=/session/home"},
	}
	plan, failed, _ := planWith(t, spec, WithDefaultShell(func() (string, error) {
		return "/bin/zsh", nil
	}))
	if failed {
		t.Fatal("the zsh plan reported a failure")
	}
	if !containsEnv(plan.Spec, "NETCATTY_USER_ZDOTDIR", "/session/home") {
		t.Fatalf("the session HOME was ignored: %v", plan.Spec.Env)
	}
}

func TestPlanLocalSkipsUnsupportedExplicitShells(t *testing.T) {
	cases := map[string][]string{
		"sh":                   {"/bin/sh"},
		"non-shell":            {"/usr/bin/python3"},
		"command":              {"/bin/zsh", "-c", "echo hi"},
		"script":               {"/bin/bash", "script.sh"},
		"stdin":                {"/bin/bash", "-s"},
		"no rc":                {"/bin/bash", "--norc"},
		"no profile":           {"/bin/bash", "--noprofile"},
		"zsh -f":               {"/bin/zsh", "-f"},
		"rcfile":               {"/bin/bash", "--rcfile", "/tmp/rc"},
		"init file":            {"/bin/bash", "--init-file", "/tmp/rc"},
		"fish no rc":           {"/bin/fish", "--no-config"},
		"fish script":          {"/bin/fish", "script.fish"},
		"fish private":         {"/bin/fish", "--private"},
		"fish private short":   {"/bin/fish", "-P"},
		"fish private cluster": {"/bin/fish", "-lP"},
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

func TestPlanLocalSkipsAnUnsupportedDefaultShell(t *testing.T) {
	for name, resolve := range map[string]func() (string, error){
		"sh":    func() (string, error) { return "/bin/sh", nil },
		"error": func() (string, error) { return "", os.ErrNotExist },
		"empty": func() (string, error) { return "", nil },
	} {
		name, resolve := name, resolve
		t.Run(name, func(t *testing.T) {
			spec := appsession.StartSpec{Kind: appsession.KindLocal}
			dir := t.TempDir()
			plan, failed := NewPlanner(WithDefaultShell(resolve), WithTempDir(func() (string, error) {
				t.Fatal("an unsupported default shell created a temporary directory")
				return dir, nil
			})).Plan(spec)
			if failed || plan.Cleanup != nil || len(plan.Spec.Env) != 0 {
				t.Fatalf("plan = %+v, failed = %t", plan, failed)
			}
		})
	}
}

func TestPlanLocalReportsATemporaryDirectoryFailureSoftly(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindLocal, Env: []string{"KEEP=1"}}
	plan, failed := NewPlanner(
		WithDefaultShell(func() (string, error) { return "/bin/zsh", nil }),
		WithTempDir(func() (string, error) { return "", os.ErrPermission }),
	).Plan(spec)
	if !failed {
		t.Fatal("the plan did not report the failure")
	}
	if plan.Cleanup != nil || !containsEnv(plan.Spec, "KEEP", "1") || len(plan.Spec.Env) != 1 {
		t.Fatalf("the failed plan changed the StartSpec: %+v", plan.Spec)
	}
}

func TestPlanLocalReportsAWriteFailureSoftly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "missing")
	spec := appsession.StartSpec{Kind: appsession.KindLocal}
	plan, failed := NewPlanner(
		WithDefaultShell(func() (string, error) { return "/bin/zsh", nil }),
		WithTempDir(func() (string, error) { return dir, nil }),
	).Plan(spec)
	if !failed {
		t.Fatal("the plan did not report the write failure")
	}
	if plan.Cleanup != nil || len(plan.Spec.Env) != 0 {
		t.Fatalf("the failed plan changed the StartSpec: %+v", plan.Spec)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the failed directory was left behind: %v", err)
	}
}

// TestScriptsNeverContainASingleQuote covers only the startup scripts the
// remote wrapper carries. The wrapper passes them inside a single-quoted POSIX
// command, so one single quote would break every supported remote shell. The
// nushell and xonsh hooks are written to their own startup files and never
// reach the wrapper: the transport keeps the plain login shell for those
// shells.
func TestScriptsNeverContainASingleQuote(t *testing.T) {
	scripts := map[string]string{
		"zsh env":    zshEnvScript,
		"zsh rc":     zshRC(),
		"zsh source": zshSourceScript(".zprofile"),
		"bash":       bashStartupScript(),
		"fish":       fishHookText,
	}
	for name, script := range scripts {
		if strings.Contains(script, "'") {
			t.Fatalf("%s contains a single quote, which the remote wrapper cannot carry: %q", name, script)
		}
	}
	remote := remoteWrapperScript()
	if strings.Contains(remote, "'") {
		t.Fatal("the remote wrapper script contains a single quote")
	}
}

// TestHooksKeepAnUnknownExitUnknown covers the nushell and xonsh finish marks.
// A shell that has no exit code must emit a bare OSC 133 D; defaulting to 0
// would record a known success that never happened.
func TestHooksKeepAnUnknownExitUnknown(t *testing.T) {
	if strings.Contains(nuHookText, "LAST_EXIT_CODE? | default 0") {
		t.Fatal("the nushell hook defaults an absent exit code to 0")
	}
	for name, script := range map[string]string{"nushell": nuHookText, "xonsh": xonshHookText} {
		if !strings.Contains(script, "133;D") {
			t.Fatalf("%s does not emit a finish mark", name)
		}
		if !strings.Contains(script, `133;D\u{7}`) && !strings.Contains(script, `133;D\x07`) {
			t.Fatalf("%s has no unknown-exit finish mark", name)
		}
	}
	if !strings.Contains(nuHookText, "($env.LAST_EXIT_CODE?)") {
		t.Fatal("the nushell hook does not read LAST_EXIT_CODE as an optional value")
	}
}

func TestScriptsGuardAgainstExistingIntegrations(t *testing.T) {
	scripts := map[string]string{
		"zsh":     zshHookScript,
		"bash":    bashHookScript,
		"fish":    fishHookText,
		"nushell": nuHookText,
		"xonsh":   xonshHookText,
	}
	for name, script := range scripts {
		guards := []string{
			"NETCATTY_HOOK",
			"ATUIN_SESSION",
			"STARSHIP_SHELL",
			"STARSHIP_SESSION_KEY",
			"ITERM_SHELL_INTEGRATION_INSTALLED",
		}
		for _, guard := range guards {
			if !strings.Contains(script, guard) {
				t.Fatalf("%s does not check %s", name, guard)
			}
		}
	}
	for name, script := range map[string]string{"zsh": zshHookScript, "bash": bashHookScript} {
		for _, function := range []string{"atuin_preexec", "iterm2_preexec"} {
			if !strings.Contains(script, function) {
				t.Fatalf("%s does not check the %s function", name, function)
			}
		}
	}
	if !strings.Contains(fishHookText, "functions -q atuin_preexec") {
		t.Fatal("fish does not check the atuin_preexec function")
	}
	if !strings.Contains(fishHookText, "set -q fish_private_mode") {
		t.Fatal("fish does not skip private mode")
	}
}

// The prompt handshake tells the tap the hook owns capture, so the tap does
// not finalize typed input at a shell's own prompt mark.
func TestHooksEmitThePromptHandshake(t *testing.T) {
	for name, script := range map[string]string{
		"zsh":  zshHookScript,
		"bash": bashHookScript,
		"fish": fishHookText,
	} {
		handshake := strings.Index(script, "1337;NetcattyHook;"+name)
		dMark := strings.Index(script, "133;D")
		prompt := strings.Index(script, "133;A")
		if handshake < 0 || dMark < 0 || prompt < 0 || !(dMark < handshake && handshake < prompt) {
			t.Fatalf("%s handshake order: D %d, handshake %d, A %d", name, dMark, handshake, prompt)
		}
	}
}

// The hook must not announce itself when it cannot encode the private
// sequence: a handshake without a possible NetcattyExec would disable the
// tap's Tier 2/3 fallback and capture nothing. Every script probes the
// encoder and returns before registering handlers or emitting the handshake.
func TestHooksRequireTheEncoderBeforeInstalling(t *testing.T) {
	for name, script := range map[string]string{
		"zsh":  zshHookScript,
		"bash": bashHookScript,
		"fish": fishHookText,
	} {
		probe := strings.Index(script, "__netcatty_encoder_probe")
		handshake := strings.Index(script, "1337;NetcattyHook;"+name)
		handler := strings.Index(script, "__netcatty_b64")
		if probe < 0 || handshake < 0 || handler < 0 || !(probe < handler && probe < handshake) {
			t.Fatalf("%s encoder guard order: probe %d, handler %d, handshake %d",
				name, probe, handler, handshake)
		}
		if !strings.Contains(script[probe:handshake], "return") {
			t.Fatalf("%s does not return when the encoder is missing", name)
		}
		if name != "fish" {
			// A user's set -e must not turn the failed probe into an
			// interactive shell exit.
			line := script[probe:]
			if end := strings.IndexByte(line, '\n'); end >= 0 {
				line = line[:end]
			}
			if !strings.HasSuffix(strings.TrimSpace(line), "|| true") {
				t.Fatalf("%s encoder probe is not set -e safe: %q", name, line)
			}
		}
	}
}

// Every early return in a startup script is a deliberate refusal to install:
// existing integration, missing encoder, private fish, non-interactive shell,
// or an inherited NETCATTY_HOOK without this shell's hook functions. Each one
// must announce the private skip marker instead of the handshake, so the tap
// releases typed capture to Tier 2/3 for this and every later command.
func TestSkippedHooksAnnounceTheSkip(t *testing.T) {
	for name, script := range map[string]string{
		"zsh":  zshHookScript,
		"bash": bashHookScript,
		"fish": fishHookText,
	} {
		marker := "1337;NetcattyHookSkip;" + name
		count := strings.Count(script, marker)
		if count < 5 {
			t.Fatalf("%s emits the skip marker %d times, want every refusal path", name, count)
		}
		guards := strings.Count(script, "NETCATTY_HOOK")
		if guards < 2 {
			t.Fatalf("%s does not check an inherited NETCATTY_HOOK separately", name)
		}
	}
	if count := strings.Count(remotePrelude, "1337;NetcattyHookSkip"); count < 2 {
		t.Fatalf("the remote wrapper prelude marks only %d unsupported-shell paths", count)
	}
	if strings.Contains(remotePrelude, "1337;NetcattyHook;") {
		t.Fatal("the remote wrapper prelude emits the prompt handshake")
	}
}

// bash requires GNU long options before short ones. An API-only explicit
// ["bash","-i"] plan used to become "bash -i --rcfile ... -i", which bash
// exits with status 2.
func TestPlanLocalExplicitBashPutsTheLongOptionFirst(t *testing.T) {
	spec := appsession.StartSpec{Kind: appsession.KindLocal, Command: []string{"/bin/bash", "-i"}}
	plan, failed, dir := planWith(t, spec)
	if failed {
		t.Fatal("the explicit bash plan reported a failure")
	}
	command := plan.Spec.Command
	if len(command) < 5 || command[0] != "/bin/bash" || command[1] != "--rcfile" ||
		command[2] != filepath.Join(dir, "bashrc") {
		t.Fatalf("command = %v, want --rcfile immediately after the program name", command)
	}
	if joined := strings.Join(command[3:], " "); strings.Contains(joined, "--rcfile") {
		t.Fatalf("the long option did not move to the front: %v", command)
	}
}

func TestHooksAppendToTheUsersMechanisms(t *testing.T) {
	zsh := zshHookScript
	for _, want := range []string{
		"add-zsh-hook precmd __netcatty_precmd",
		"add-zsh-hook preexec __netcatty_preexec",
		"precmd_functions+=(__netcatty_precmd)",
		"preexec_functions+=(__netcatty_preexec)",
	} {
		if !strings.Contains(zsh, want) {
			t.Fatalf("zsh hook is missing %q", want)
		}
	}

	bash := bashHookScript
	for _, want := range []string{
		`PROMPT_COMMAND=(__netcatty_save_status "${PROMPT_COMMAND[@]}" __netcatty_precmd)`,
		`__netcatty_save_status;${__netcatty_prev_prompt:+ $__netcatty_prev_prompt;} __netcatty_precmd`,
		"preexec_functions+=(__netcatty_preexec)",
		"precmd_functions=(__netcatty_save_status \"${precmd_functions[@]}\" __netcatty_precmd)",
		"__netcatty_prev_debug=$(trap -p DEBUG",
		`trap "__netcatty_debug" DEBUG`,
		`"${BASH_COMMAND:-}" != "__netcatty_save_status"`,
		"__netcatty_ready=0",
	} {
		if !strings.Contains(bash, want) {
			t.Fatalf("bash hook is missing %q", want)
		}
	}

	fish := fishHookText
	for _, want := range []string{
		"--on-event fish_preexec",
		"--on-event fish_postexec",
		"--on-event fish_prompt",
		"set -gx NETCATTY_HOOK 1",
	} {
		if !strings.Contains(fish, want) {
			t.Fatalf("fish hook is missing %q", want)
		}
	}
}

func TestHooksEmitThePrivateSequenceBeforeTheStandardMarks(t *testing.T) {
	for name, script := range map[string]string{
		"zsh":     zshHookScript,
		"bash":    bashHookScript,
		"fish":    fishHookText,
		"nushell": nuHookText,
		"xonsh":   xonshHookText,
	} {
		private := strings.Index(script, "1337;NetcattyExec")
		standard := strings.Index(script, "133;C")
		if private < 0 || standard < 0 || private > standard {
			t.Fatalf("%s emits the private sequence at %d and OSC 133 C at %d", name, private, standard)
		}
	}
	for name, script := range map[string]string{
		"zsh":  zshHookScript,
		"bash": bashHookScript,
		"fish": fishHookText,
	} {
		if !strings.Contains(script, "tr \"+/\" \"-_\"") || !strings.Contains(script, "tr -d \"=\"") {
			t.Fatalf("%s does not emit unpadded base64url", name)
		}
	}
}

func TestZshSourcesTheUserStartupBeforeInstallingTheHook(t *testing.T) {
	rc := zshRC()
	source := strings.Index(rc, `source "$ZDOTDIR/.zshrc"`)
	hook := strings.Index(rc, "NETCATTY_HOOK")
	if source < 0 || hook < 0 || source > hook {
		t.Fatalf("source at %d, hook at %d", source, hook)
	}
	env := zshEnvScript
	if !strings.Contains(env, `source "$ZDOTDIR/.zshenv"`) {
		t.Fatal(".zshenv is not sourced")
	}
	if !strings.Contains(env, `ZDOTDIR="${NETCATTY_HOOK_DIR:-$HOME}"`) {
		t.Fatal("ZDOTDIR is not kept on the session directory")
	}
}

func TestRemoteWrapperRunsTheUsersShellAndCleansUp(t *testing.T) {
	script := remoteWrapperScript()
	for _, want := range []string{
		"netcatty_shell=${SHELL:-}",
		"getent passwd",
		`netcatty_shell=/bin/sh`,
		`case "$netcatty_name" in`,
		`exec "$netcatty_shell" -l`,
		`mktemp -d`,
		`trap cleanup EXIT`,
		`rm -rf "$netcatty_dir"`,
		`"$netcatty_shell" -l`,
		`"$netcatty_shell" --rcfile`,
		`--init-command`,
		"netcatty_status=$?",
		"exit $netcatty_status",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote wrapper is missing %q", want)
		}
	}
	if strings.Contains(script, `$HOME/`) {
		t.Fatal("the remote wrapper writes inside the user's home directory")
	}
	if strings.Count(script, "NETCATTY_EOF")%2 != 0 {
		t.Fatal("a heredoc is unbalanced")
	}
}

// TestRemotePreludeFindsTheLoginShellWithoutGetent covers the BSD and macOS
// hosts that have no getent: the wrapper reads the login shell from
// /etc/passwd before falling back to /bin/sh, using only POSIX sh syntax.
func TestRemotePreludeFindsTheLoginShellWithoutGetent(t *testing.T) {
	for _, want := range []string{
		"netcatty_shell=${SHELL:-}",
		"getent passwd",
		"netcatty_uid=$(id -u)",
		"IFS=: read -r",
		"done < /etc/passwd",
		`netcatty_shell=/bin/sh`,
	} {
		if !strings.Contains(remotePrelude, want) {
			t.Fatalf("the remote prelude is missing %q", want)
		}
	}
	for _, bashism := range []string{"[[", "local ", "function "} {
		if strings.Contains(remotePrelude, bashism) {
			t.Fatalf("the remote prelude is not POSIX sh: found %q", bashism)
		}
	}
	passwd := strings.Index(remotePrelude, "done < /etc/passwd")
	fallback := strings.Index(remotePrelude, `netcatty_shell=/bin/sh`)
	if passwd < 0 || fallback < 0 || passwd > fallback {
		t.Fatalf("the passwd lookup at %d must precede the /bin/sh fallback at %d", passwd, fallback)
	}
}

func TestRemoteWrapperCommandIsOneQuotedWord(t *testing.T) {
	command := remoteWrapperCommand()
	if !strings.HasPrefix(command, "exec /bin/sh -c '") || !strings.HasSuffix(command, "'") {
		t.Fatalf("command = %q", command)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(command, "exec /bin/sh -c '"), "'")
	if inner != remoteWrapperScript() {
		t.Fatal("the quoted command does not carry the wrapper script")
	}
	if got := quotePOSIX("a'b"); got != `'a'\''b'` {
		t.Fatalf("quotePOSIX = %q", got)
	}
}

func TestRemoteFileQuotesItsDelimiter(t *testing.T) {
	file := remoteFile(".zshrc", "body $HOME\n")
	want := "cat > \"$netcatty_dir/.zshrc\" <<\"NETCATTY_EOF\"\nbody $HOME\nNETCATTY_EOF\n"
	if file != want {
		t.Fatalf("remoteFile = %q", file)
	}
}
