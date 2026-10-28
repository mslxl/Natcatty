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

		// Fail closed: a command word that is still dynamic after expansion
		// cannot be matched against the table, so it is blocked by name.
		{"unresolved command variable", `$CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved braced command variable", `${RUNNER} -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved variable after assignment", `x=echo; $RUNNER -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after a chain", `true && $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved command after a pipe", `echo ok | $CMD -rf /tmp/x`, ShellKindPOSIX, "common.unresolvable-command"},
		{"unresolved powershell variable command", `$cmd -Recurse -Force C:\temp`, ShellKindPowerShell, "common.unresolvable-command"},
		{"unresolved cmd variable command", `%RUN% -rf C:\temp`, ShellKindCmd, "common.unresolvable-command"},
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
