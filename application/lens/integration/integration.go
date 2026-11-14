// Package integration starts the user's shell with Terminal-style shell
// integration: OSC 133 command marks (A prompt start, B prompt end,
// C command start with the typed line, D command end with its status), the
// OSC 7 working directory, and the pager hints that keep the commands the
// lenses parse from paging or redrawing their progress.
//
// Shell is the only entry point main.go needs:
//
//	prog, args, env := integration.Shell(defaultShell(), os.Environ())
//	sess, err := pty.Start(pty.Options{Shell: append([]string{prog}, args...), Env: env, ...})
//
// When no integration is available for the shell (cmd.exe, an unknown
// program), the program and arguments are returned unchanged and only the
// base environment is added.
package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Shell returns the program, its arguments and the environment that start
// shell with the integration installed.
//
// shell is the program and its arguments; empty runs the platform's shell
// ($SHELL, else /bin/zsh on macOS and /bin/sh elsewhere; pwsh.exe,
// powershell.exe, %COMSPEC% or cmd.exe on Windows). env is the base
// environment (usually os.Environ()), which the returned extraEnv extends;
// variables in extraEnv replace the ones in env.
func Shell(shell []string, env []string) (prog string, args []string, extraEnv []string) {
	prog, args = resolve(shell)
	if prog == "" {
		return "", nil, mergeEnv(env, baseEnv())
	}
	base := append(baseEnv(), hints()...)
	switch shellKind(prog) {
	case kindPowerShell:
		return prog, append(args, "-NoLogo", "-NoExit", "-Command", PowerShellScript()), mergeEnv(env, base)
	case kindBash:
		if rc := writeScript("bash-rcfile", BashScript()); rc != "" {
			return prog, append(args, "--rcfile", rc), mergeEnv(env, base)
		}
	case kindZsh:
		if dir := zshZDOTDIR(); dir != "" {
			e := append(base, "ZDOTDIR="+dir, "KITTY_ZDOTDIR="+homeDir())
			return prog, args, mergeEnv(env, e)
		}
	case kindFish:
		if init := writeScript("fish-init.fish", FishScript()); init != "" {
			return prog, append(args, "-C", "source "+init), mergeEnv(env, base)
		}
	case kindPOSIX:
		if f := writeScript("sh-env", POSIXScript()); f != "" {
			e := append(base, "ENV="+f)
			return prog, args, mergeEnv(env, e)
		}
	}
	// No integration for this shell: a plain shell, with the terminal's
	// variables only.
	return prog, args, mergeEnv(env, baseEnv())
}

// baseEnv is what the terminal sets whatever the shell is.
func baseEnv() []string {
	return []string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"TERM_PROGRAM=Natcatty",
		"NATCATTY_LENSES=1",
	}
}

// hints keeps the commands the lenses parse from paging or redrawing their
// progress, as Terminal's shell integration does (findings/lens-conditions.md
// §2: GIT_PAGER, GH_PAGER, MANPAGER, SYSTEMD_PAGER, AWS_PAGER, BAT_PAGING,
// PSQL_PAGER, CLICOLOR and BUILDKIT_PROGRESS).
func hints() []string {
	return []string{
		"PAGER=cat",
		"GIT_PAGER=cat",
		"GH_PAGER=cat",
		"MANPAGER=cat",
		"SYSTEMD_PAGER=cat",
		"AWS_PAGER=",
		"BAT_PAGING=never",
		"PSQL_PAGER=cat",
		"BUILDKIT_PROGRESS=plain",
		"CLICOLOR=1",
	}
}

type shellKindT int

const (
	kindUnknown shellKindT = iota
	kindPowerShell
	kindBash
	kindZsh
	kindFish
	kindPOSIX
)

// shellKind classifies a program by its file name.
func shellKind(prog string) shellKindT {
	name := strings.ToLower(filepath.Base(prog))
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "pwsh", "powershell":
		return kindPowerShell
	case "bash":
		return kindBash
	case "zsh":
		return kindZsh
	case "fish":
		return kindFish
	case "sh", "dash", "ash", "ksh", "mksh":
		return kindPOSIX
	}
	return kindUnknown
}

// resolve returns the program and its arguments, filling in the platform's
// shell when shell is empty.
func resolve(shell []string) (string, []string) {
	if len(shell) > 0 && shell[0] != "" {
		return shell[0], shell[1:]
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"pwsh.exe", "powershell.exe", os.Getenv("COMSPEC"), "cmd.exe"} {
			if name == "" {
				continue
			}
			if p, err := exec.LookPath(name); err == nil {
				return p, nil
			}
		}
		return "", nil
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, nil
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh", nil
	}
	return "/bin/sh", nil
}

// mergeEnv returns base with extra applied over it, as "KEY=value" strings
// (extra wins; keys are matched case-insensitively, as Windows treats them).
func mergeEnv(base, extra []string) []string {
	out := make([]string, 0, len(base)+len(extra))
	index := make(map[string]int, len(base)+len(extra))
	add := func(kv string) {
		if kv == "" {
			return
		}
		key, _, _ := strings.Cut(kv, "=")
		if i, ok := index[strings.ToUpper(key)]; ok {
			out[i] = kv
			return
		}
		index[strings.ToUpper(key)] = len(out)
		out = append(out, kv)
	}
	for _, kv := range base {
		add(kv)
	}
	for _, kv := range extra {
		add(kv)
	}
	return out
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
