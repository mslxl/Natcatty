package mcp

import (
	"strings"
	"testing"
)

// TestCommandBlocklistCanonicalBypasses is the rework matrix for ticket 58:
// every case below was measured against the literal-token matcher as an
// executing bypass, or is a neighbouring obfuscation the canonicalizer must
// also cover. Each must be blocked with a typed rule that names the hazard.
func TestCommandBlocklistCanonicalBypasses(t *testing.T) {
	type testCase struct {
		name    string
		command string
		shell   ShellKind
		rule    string
	}
	cases := []testCase{
		// Measured POSIX bypasses: the quoted or expanded word really runs
		// rm -rf in a POSIX shell.
		{"double quoted command word", `"rm" -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"single quoted command word", `'rm' -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"empty quotes inside the command word", `r''m -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"IFS between command and flags", `rm$IFS-rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"braced IFS between command and flags", `rm${IFS}-rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"assignment then variable command", `x=rm; $x -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"quoted assignment then variable command", `x="rm"; ${x} -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"transitive assignment", `a=rm; b=$a; $b -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"backslash newline continuation", "r\\\nm -rf /tmp/x", ShellKindPOSIX, "common.rm-recursive-force"},
		{"backslash escaped command word", `\rm -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"partially quoted command word", `"r"m -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"ansi-c quoted fragment", `$'r'm -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"ansi-c hex encoded word", `$'\x72\x6d' -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"command wrapper", `command rm -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"env wrapper", `env rm -rf /tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"bash -c with quoted payload", `bash -c '"rm" -rf x'`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"fish IFS form", `rm$IFS-rf /tmp/x`, ShellKindFish, "common.rm-recursive-force"},

		// PowerShell: backtick escapes are the shell's own quoting, and a
		// backtick at end of line is a continuation.
		{"powershell backtick escaped parameter", "Remove-Item -`Re`curse -Force C:\\temp", ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell backtick newline continuation", "Remove-Item -Rec`\nurse -Force C:\\temp", ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell quoted command word", `& 'Remove-Item' -Recurse -Force C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell quoted command and parameter", `"Remove-Item" "-Recurse" "-Force" C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell backtick escaped alias", "ri -`r -`fo C:\\temp", ShellKindPowerShell, "powershell.remove-recursive-force"},

		// cmd: caret is the escape character and double quotes group a name.
		{"cmd caret escaped command word", `r^m -rf C:\temp`, ShellKindCmd, "common.rm-recursive-force"},
		{"cmd quoted command word", `"rm" -rf C:\temp`, ShellKindCmd, "common.rm-recursive-force"},
		{"cmd caret newline continuation", "r^\nm -rf C:\\temp", ShellKindCmd, "common.rm-recursive-force"},

		// Rework 2: shell expansion classes the first canonicalizer still
		// missed. Every one of these executed against the independent
		// acceptance's disposable marker.
		{"posix brace expanded command word", `{rm,-rf,/tmp/x}`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"posix nested brace expansion", `{r{m,},-rf,/tmp/x}`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"posix brace expansion with a variable path", `D=/tmp/x; {rm,-rf,$D}`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"posix second brace group", `echo {a,b}; {rm,-rf,/tmp/x}`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"posix brace group after a pipeline", `echo {a,b} | {bash,-c,'rm -rf /tmp/x'}`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"printf escape piped to a shell", `printf 'r\155 -rf %s' /tmp/x | sh`, ShellKindPOSIX, "posixNative.pipe-to-shell"},
		{"printf piped to bash", `printf '%s' 'echo hello' | bash`, ShellKindPOSIX, "posixNative.pipe-to-shell"},
		{"command piped to an absolute shell", `echo cm0= | /bin/sh`, ShellKindPOSIX, "posixNative.pipe-to-shell"},
		{"command piped to sudo sh", `printf x | sudo sh`, ShellKindPOSIX, "posixNative.pipe-to-shell"},
		{"command piped to sudo with an option", `printf x | sudo -u root sh`, ShellKindPOSIX, "posixNative.pipe-to-shell"},
		{"command piped to env sh", `printf x | env FOO=1 sh`, ShellKindPOSIX, "posixNative.pipe-to-shell"},
		{"command piped to absolute env sh", `printf x | /usr/bin/env sh`, ShellKindPOSIX, "posixNative.pipe-to-shell"},
		{"command piped to command sh", `printf x | command sh`, ShellKindPOSIX, "posixNative.pipe-to-shell"},
		{"find delete", `find /tmp/x -delete`, ShellKindPOSIX, "posix.find-delete"},
		{"find delete with a predicate", `find /tmp/x -name '*.tmp' -delete`, ShellKindFish, "posix.find-delete"},
		{"powershell folded command word", `& (''+'Remove'+'-Item') -Recurse -Force C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell folded command word with double quotes", `& ("Remove"+"-Item") -Recurse -Force C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell aliased command", `Set-Alias zz Remove-Item; zz -Recurse -Force C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell new-alias command", `New-Alias -Name zz -Value Remove-Item; zz -Recurse -Force C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell sal command", `sal zz Remove-Item; zz -Recurse -Force C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell alias equals command", `alias zz=Remove-Item; zz -Recurse -Force C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell alias definition only", `Set-Alias zz Remove-Item`, ShellKindPowerShell, "powershell.alias-to-blocked-command"},
		{"cmd comspec indirection", `%ComSpec% /c del /f /q C:\temp\marker.txt`, ShellKindCmd, "common.force-delete"},
		{"powershell comspec indirection", `$env:ComSpec /c del /f /q C:\temp\marker.txt`, ShellKindPowerShell, "common.force-delete"},
		{"cmd nested under powershell", `cmd /c r^m -rf C:\temp`, ShellKindPowerShell, "common.rm-recursive-force"},
		{"cmd nested under powershell with comspec", `cmd /c %ComSpec% /c del /f /q C:\temp\marker.txt`, ShellKindPowerShell, "common.force-delete"},
		{"cmd keep-open nested payload", `cmd /k r^m -rf C:\temp`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"cmd undocumented r switch payload", `cmd /r r^m -rf C:\temp`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"cmd glued switch payload", `cmd /c"r^m -rf C:\temp"`, ShellKindPOSIX, "common.rm-recursive-force"},
		{"cmd glued switch payload under cmd", `cmd /c"r^m -rf C:\temp"`, ShellKindCmd, "common.rm-recursive-force"},
		{"cmd keep-open caret under cmd", `cmd /k r^m -rf C:\temp`, ShellKindCmd, "common.rm-recursive-force"},
		{"cmd recursive directory delete", `rd /s /q C:\temp\dir`, ShellKindCmd, "common.force-delete"},

		// Fail closed: a command word that is still dynamic after expansion
		// cannot be matched against the table, so it is blocked by name.
		{"unresolved command variable", `$CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved braced command variable", `${RUNNER} -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved variable after assignment", `x=echo; $RUNNER -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after a chain", `true && $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after a pipe", `echo ok | $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved powershell variable command", `$cmd -Recurse -Force C:\temp`, ShellKindPowerShell, "common.unresolvable-command"},
		{"unresolved cmd variable command", `%RUN% -rf C:\temp`, ShellKindCmd, "common.unresolvable-command"},
		{"unresolved cmd keep-open variable", `cmd /k $CMD`, ShellKindPowerShell, "common.unresolvable-command"},
		{"unresolved cmd r-switch variable", `cmd /r $CMD`, ShellKindPowerShell, "common.unresolvable-command"},
		{"unresolved cmd glued variable", `cmd /c"$CMD"`, ShellKindPowerShell, "common.unresolvable-command"},
		{"unmatched backtick in command word", "x`y -rf /tmp/x", ShellKindPOSIX, "common.unresolvable-command"},

		// A wrapper or shell construct executes the word after it, so a
		// dynamic command there is still a command position.
		{"unresolved command after command wrapper", `command $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after env wrapper", `env $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after env assignment", `env FOO=1 $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after sudo", `sudo $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after sudo option value", `sudo -u root $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after exec", `exec $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after nohup", `nohup $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after time", `time $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after nice", `nice -n 10 $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command through sh -c", `sh -c "$CMD -rf /tmp/x"`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command through bash -lc", `bash -lc "$CMD -rf /tmp/x"`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command through eval", `eval "$CMD -rf /tmp/x"`, ShellKindPOSIX, "posix.eval"},
		{"unresolved command inside a subshell", `( $CMD -rf /tmp/x )`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command attached to a subshell", `($CMD -rf /tmp/x)`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command in an if branch", `if true; then $CMD -rf /tmp/x; fi`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command in a loop body", `for x in a; do $CMD -rf /tmp/x; done`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command in a while condition", `while $CMD; do :; done`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after find -exec", `find . -exec $CMD {} ;`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after ssh", `ssh host $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after timeout", `timeout 30 $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after chroot", `chroot /tmp $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command through su -c", `su -c "$CMD -rf /tmp/x"`, ShellKindPOSIX, "common.unresolvable-command"},

		// Rework 2: neighbours the independent acceptance recorded as typed
		// blocked before this rework must stay blocked.
		{"powershell get-command invocation", `& (Get-Command Remove-Item) -Recurse -Force C:\temp`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell folded invoke-expression", `Invoke-Expression ('Remove'+'-Item -Recurse -Force C:\temp')`, ShellKindPowerShell, "powershell.remove-recursive-force"},
		{"powershell foreach variable invocation", `Get-Command Remove-Item | ForEach-Object { & $_ -Recurse -Force C:\temp }`, ShellKindPowerShell, "common.unresolvable-command"},
		{"powershell stored command invocation", `$c = Get-Command Remove-Item; & $c -Recurse -Force C:\temp`, ShellKindPowerShell, "common.unresolvable-command"},
		{"posix parameter expansion command", `${PATH:+rm} -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"posix default expansion command", `${x:-rm} -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"posix ifs assignment then variable path", `IFS=,; rm$IFS-rf$IFS/tmp/x`, ShellKindPOSIX, "common.rm-recursive-force"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rule, matched := CheckCommandBlocklist(test.command, test.shell)
			if !matched {
				t.Fatalf("CheckCommandBlocklist(%q, %q) allowed; want rule %s", test.command, test.shell, test.rule)
			}
			if rule.Name != test.rule {
				t.Fatalf("CheckCommandBlocklist(%q, %q) rule = %q; want %q", test.command, test.shell, rule.Name, test.rule)
			}
		})
	}
}

// TestCommandBlocklistCanonicalizationKeepsArgumentsUsable proves the
// canonicalizer does not block ordinary arguments that merely contain a
// variable, an IFS reference, or a percent-wrapped name.
func TestCommandBlocklistCanonicalizationKeepsArgumentsUsable(t *testing.T) {
	allowed := []struct {
		name    string
		command string
		shell   ShellKind
	}{
		{"echo a variable", `echo $HOME`, ShellKindPOSIX},
		{"grep a quoted variable", `grep "$HOME/x" .`, ShellKindPOSIX},
		{"printf a quoted variable", `printf '%s\n' "$HOME"`, ShellKindPOSIX},
		{"variable assigned then used as an argument", `x=1; echo $x`, ShellKindPOSIX},
		{"IFS as an argument value", `printf '%s' "$IFS"`, ShellKindPOSIX},
		{"single quoted dollar", `echo '$HOME'`, ShellKindPOSIX},
		{"escaped dollar", `echo \$HOME`, ShellKindPOSIX},
		{"variable in a chained argument", `cd /tmp && ls $HOME`, ShellKindPOSIX},
		{"powershell environment variable argument", `Write-Output $env:HOME`, ShellKindPowerShell},
		{"powershell quoted hash argument", `Write-Output '# not a comment'`, ShellKindPowerShell},
		{"cmd percent argument", `echo %TEMP%`, ShellKindCmd},
		{"cmd quoted percent argument", `echo "%TEMP%\file"`, ShellKindCmd},
		{"plain listing", `ls -la /tmp`, ShellKindPOSIX},
		{"rm without both flags", `rm -r /tmp/x`, ShellKindPOSIX},
		{"rm flags split across commands", `rm -r /tmp/x; rm -f /tmp/y`, ShellKindPOSIX},

		// A separator inside a quoted argument is data, not a command
		// boundary; the rework must not regress these ordinary forms.
		{"separator inside a double quoted argument", `echo "a; $HOME"`, ShellKindPOSIX},
		{"pipe inside a double quoted argument", `grep "a|$HOME" file`, ShellKindPOSIX},
		{"chain inside a double quoted argument", `git commit -m "fix; $TICKET"`, ShellKindPOSIX},
		{"and inside a double quoted argument", `echo "a && $HOME"`, ShellKindPOSIX},
		{"separator inside a single quoted argument", `echo 'a; $HOME'`, ShellKindPOSIX},
		{"cmd separator inside a quoted argument", `echo "a; %TEMP%"`, ShellKindCmd},
		{"dynamic argument through sh -c", `sh -c 'echo $HOME'`, ShellKindPOSIX},
		{"dynamic remote argument", `ssh host echo $HOME`, ShellKindPOSIX},
		{"dynamic argument after timeout", `timeout 30 echo $HOME`, ShellKindPOSIX},
		{"dynamic argument after sudo", `sudo -u root echo $HOME`, ShellKindPOSIX},
		{"dynamic find argument", `find . -name '$HOME'`, ShellKindPOSIX},

		// Rework 2: the fail-closed rule must not judge ordinary read and
		// assignment statements in PowerShell as dynamic commands, and the new
		// expansions must not touch ordinary command lines.
		{"powershell environment variable expression", `$env:PATH`, ShellKindPowerShell},
		{"powershell environment variable expression braced", `${env:PATH}`, ShellKindPowerShell},
		{"powershell variable expression", `$D`, ShellKindPowerShell},
		{"powershell quoted assignment", `$D = 'C:\Temp\natcatty target'`, ShellKindPowerShell},
		{"powershell compact assignment", `$D='C:\Temp\natcatty target'`, ShellKindPowerShell},
		{"powershell variable assignment read", `$D = $env:PATH`, ShellKindPowerShell},
		{"powershell child item", `Get-ChildItem`, ShellKindPowerShell},
		{"powershell write variable argument", `Write-Output $D`, ShellKindPowerShell},
		{"cmd echo path", `cmd /c echo %PATH%`, ShellKindCmd},
		{"cmd echo path under powershell", `cmd /c echo %PATH%`, ShellKindPowerShell},
		{"git status", `git status`, ShellKindPOSIX},
		{"sudo service status", `sudo systemctl status nginx`, ShellKindPOSIX},
		{"npm test", `npm test`, ShellKindPOSIX},
		{"find with a name predicate", `find . -name '*.go'`, ShellKindPOSIX},
		{"brace expansion without a hazard", `ls {a,b}`, ShellKindPOSIX},
		{"pipe to a non-shell", `find . -name '*.go' | head`, ShellKindPOSIX},
		{"powershell alias to a safe command", `Set-Alias list Get-ChildItem; list`, ShellKindPowerShell},
		{"powershell set-alias unrelated", `Set-Alias gci Get-ChildItem`, ShellKindPowerShell},
		{"cmd delete without force", `del C:\temp\one.txt`, ShellKindCmd},
		{"cmd erase without force", `erase C:\temp\one.txt`, ShellKindCmd},
	}
	for _, test := range allowed {
		t.Run(test.name, func(t *testing.T) {
			if rule, matched := CheckCommandBlocklist(test.command, test.shell); matched {
				t.Fatalf("CheckCommandBlocklist(%q, %q) blocked by %s; want allowed", test.command, test.shell, rule.Name)
			}
		})
	}
}

// TestCommandBlocklistCanonicalRulesKeepFirstMatch proves a specific rule
// still wins over the fail-closed rule when both could describe the string.
func TestCommandBlocklistCanonicalRulesKeepFirstMatch(t *testing.T) {
	rule, matched := CheckCommandBlocklist(`x=rm; $x -rf /tmp/x; $CMD`, ShellKindPOSIX)
	if !matched || rule.Name != "common.rm-recursive-force" {
		t.Fatalf("rule = %q/%v, want the specific rm rule", rule.Name, matched)
	}
	if _, matched := CheckCommandBlocklist(`$CMD; echo ok`, ShellKindPOSIX); !matched {
		t.Fatal("an unresolved command must be blocked")
	}
}

// TestCanonicalizeHelpers exposes the canonical text the matcher sees so a
// regression in quoting or expansion is diagnosed directly.
func TestCanonicalizeHelpers(t *testing.T) {
	cases := []struct {
		shell ShellKind
		in    string
		want  string
	}{
		{ShellKindPOSIX, `"rm" -rf /tmp/x`, `rm -rf /tmp/x`},
		{ShellKindPOSIX, `r''m -rf`, `rm -rf`},
		{ShellKindPOSIX, `rm$IFS-rf`, `rm -rf`},
		{ShellKindPOSIX, `rm${IFS}-rf`, `rm -rf`},
		{ShellKindPOSIX, `x=rm; $x -rf`, `x=rm; rm -rf`},
		{ShellKindPOSIX, "r\\\nm -rf", `rm -rf`},
		{ShellKindPOSIX, `$'r'm -rf`, `rm -rf`},
		{ShellKindPOSIX, `$'\x72\x6d' -rf`, `rm -rf`},
		{ShellKindPowerShell, "Remove-Item -`Re`curse -Force", `Remove-Item -Recurse -Force`},
		{ShellKindPowerShell, "Remove-Item -Rec`\nurse", `Remove-Item -Recurse`},
		{ShellKindPowerShell, `& 'Remove-Item' -Recurse`, `& Remove-Item -Recurse`},
		{ShellKindCmd, `r^m -rf`, `rm -rf`},
		{ShellKindCmd, `"rm" -rf`, `rm -rf`},
	}
	for _, test := range cases {
		got := strings.TrimSpace(CanonicalizeCommand(test.in, test.shell))
		if got != test.want {
			t.Fatalf("CanonicalizeCommand(%q, %q) = %q; want %q", test.in, test.shell, got, test.want)
		}
	}
}
