package shellhook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The scripts are data, not assembled application strings. Every script uses
// double quotes only: the remote wrapper is passed as one single-quoted
// argument to the login shell, so a single quote inside would break the
// wrapper under some login shells. Tests enforce that rule.
//
// Each installed script prints, in order:
//
//	OSC 1337;NetcattyExec;<shell>;<base64url(cwd)>;<base64url(command)>
//	OSC 133 C            (preexec)
//	OSC 133 D;<exit>     (postexec)
//	OSC 133 A / OSC 7 / OSC 133 B   (prompt)
//
// A script that deliberately installs nothing prints one private skip marker
// instead, and never the prompt handshake:
//
//	OSC 1337;NetcattyHookSkip;<shell>
//
// The tap then keeps the native or typed fallback for this and later commands
// instead of reserving a first command for a hook that will never speak.
//
// base64 is the system utility; when it is unavailable the private sequence
// is skipped and the standard marks still work, so the hook fails softly.

// zshEnvScript sources the user's .zshenv and then keeps zsh reading the
// session-scoped files from the temporary ZDOTDIR.
const zshEnvScript = `__netcatty_zdotdir=${NETCATTY_USER_ZDOTDIR:-$HOME}
ZDOTDIR="$__netcatty_zdotdir"
if [[ -f "$ZDOTDIR/.zshenv" ]]; then
  source "$ZDOTDIR/.zshenv"
fi
if [[ -n ${ZDOTDIR:-} && $ZDOTDIR != ${NETCATTY_HOOK_DIR:-} ]]; then
  __netcatty_zdotdir=$ZDOTDIR
fi
export NETCATTY_USER_ZDOTDIR="$__netcatty_zdotdir"
ZDOTDIR="${NETCATTY_HOOK_DIR:-$HOME}"
`

// zshSourceScript sources one of the user's remaining zsh startup files with
// ZDOTDIR pointing at the user's directory, then restores it.
func zshSourceScript(file string) string {
	return fmt.Sprintf(`__netcatty_zdotdir=${NETCATTY_USER_ZDOTDIR:-$HOME}
ZDOTDIR="$__netcatty_zdotdir"
if [[ -f "$ZDOTDIR/%s" ]]; then
  source "$ZDOTDIR/%s"
fi
ZDOTDIR="${NETCATTY_HOOK_DIR:-$HOME}"
`, file, file)
}

// zshRCInstall is appended to the user's .zshrc run. It appends to the
// standard hook arrays through add-zsh-hook when available.
const zshHookScript = `if [[ -n ${NETCATTY_HOOK:-} ]]; then
  if (( ! ${+functions[__netcatty_precmd]} )); then
    printf "\033]1337;NetcattyHookSkip;zsh\007"
  fi
  return 0
fi
if [[ -n ${ATUIN_SESSION:-} || -n ${STARSHIP_SHELL:-} || -n ${STARSHIP_SESSION_KEY:-} || -n ${ITERM_SHELL_INTEGRATION_INSTALLED:-} ]]; then
  printf "\033]1337;NetcattyHookSkip;zsh\007"
  return 0
fi
if (( ${+functions[atuin_preexec]} || ${+functions[iterm2_preexec]} || ${+functions[iterm2_prompt_mark]} )); then
  printf "\033]1337;NetcattyHookSkip;zsh\007"
  return 0
fi
if [[ -o interactive ]]; then
  :
else
  printf "\033]1337;NetcattyHookSkip;zsh\007"
  return 0
fi

# The hook is only useful when it can encode the private sequence. Without
# base64/tr it must install nothing, including the prompt handshake, so the
# tap keeps using native OSC or typed-input capture instead of suppressing
# the fallback for a hook that can never report a command. The || true keeps
# set -e in a user rc from exiting an interactive shell on the failed probe.
__netcatty_encoder_probe=$(printf x 2>/dev/null | command base64 2>/dev/null | command tr -d "\n" 2>/dev/null | command tr "+/" "-_" 2>/dev/null | command tr -d "=" 2>/dev/null) || true
if [[ -z $__netcatty_encoder_probe ]]; then
  printf "\033]1337;NetcattyHookSkip;zsh\007"
  return 0
fi

__netcatty_b64() {
  printf "%s" "$1" | command base64 2>/dev/null | command tr -d "\n" | command tr "+/" "-_" | command tr -d "="
}

__netcatty_preexec() {
  local __nc_line="$1" __nc_command __nc_cwd
  if [[ -z $__nc_line ]]; then
    return 0
  fi
  __nc_command=$(__netcatty_b64 "$__nc_line")
  __nc_cwd=$(__netcatty_b64 "$PWD")
  if [[ -n $__nc_command && -n $__nc_cwd ]]; then
    printf "\033]1337;NetcattyExec;zsh;%s;%s\007" "$__nc_cwd" "$__nc_command"
  fi
  printf "\033]133;C\007"
}

__netcatty_precmd() {
  local __nc_status=$?
  printf "\033]133;D;%s\007" "$__nc_status"
  printf "\033]1337;NetcattyHook;zsh\007"
  printf "\033]133;A\007"
  printf "\033]7;file://%s%s\007" "${HOST:-}" "$PWD"
  printf "\033]133;B\007"
}

if (( ! ${+functions[add-zsh-hook]} )); then
  autoload -Uz add-zsh-hook 2>/dev/null
fi
if (( ${+functions[add-zsh-hook]} )); then
  add-zsh-hook precmd __netcatty_precmd
  add-zsh-hook preexec __netcatty_preexec
else
  typeset -ga precmd_functions preexec_functions
  precmd_functions+=(__netcatty_precmd)
  preexec_functions+=(__netcatty_preexec)
fi
export NETCATTY_HOOK=1
`

// zshRC returns the session-scoped .zshrc: the user's own .zshrc first, then
// the hook.
func zshRC() string {
	return zshSourceScript(".zshrc") + zshHookScript
}

// bashStartupScript sources the user's login/profile group and interactive
// startup, then installs the hook. bash is started non-login with --rcfile
// because a login bash ignores --rcfile; sourcing the profiles here preserves
// the usual login behavior, including /etc/profile.
//
// A profile commonly sources .bashrc itself. The temporary source wrapper
// records that, so the interactive startup is sourced exactly once: a profile
// that already ran .bashrc must not run it again.
func bashStartupScript() string {
	return `__netcatty_user_home=${HOME:-}
__netcatty_bashrc_sourced=0
__netcatty_note_source() {
  case "$1" in
    "$__netcatty_user_home/.bashrc"|"$__netcatty_user_home/./.bashrc")
      __netcatty_bashrc_sourced=1
      ;;
  esac
}
__netcatty_prev_source=$(declare -f source 2>/dev/null) || __netcatty_prev_source=
__netcatty_prev_dot=$(declare -f . 2>/dev/null) || __netcatty_prev_dot=
source() {
  __netcatty_note_source "$1"
  builtin source "$@"
}
.() {
  __netcatty_note_source "$1"
  builtin source "$@"
}
if [ -f /etc/profile ]; then
  . /etc/profile
fi
if [ -n "$__netcatty_user_home" ]; then
  if [ -f "$__netcatty_user_home/.bash_profile" ]; then
    . "$__netcatty_user_home/.bash_profile"
  elif [ -f "$__netcatty_user_home/.bash_login" ]; then
    . "$__netcatty_user_home/.bash_login"
  elif [ -f "$__netcatty_user_home/.profile" ]; then
    . "$__netcatty_user_home/.profile"
  fi
fi
unset -f source .
[ -n "$__netcatty_prev_source" ] && eval "$__netcatty_prev_source"
[ -n "$__netcatty_prev_dot" ] && eval "$__netcatty_prev_dot"
if [ -n "$__netcatty_user_home" ] && [ "$__netcatty_bashrc_sourced" = "0" ] &&
  [ -f "$__netcatty_user_home/.bashrc" ]; then
  . "$__netcatty_user_home/.bashrc"
fi
` + bashHookScript
}

// bashHookScript appends to PROMPT_COMMAND and to the bash-preexec arrays when
// they exist, and otherwise chains an existing DEBUG trap. It never replaces a
// user mechanism.
const bashHookScript = `if [ -n "${NETCATTY_HOOK:-}" ]; then
  if ! declare -F __netcatty_precmd >/dev/null 2>&1; then
    printf "\033]1337;NetcattyHookSkip;bash\007"
  fi
  return 0
fi
if [ -n "${ATUIN_SESSION:-}" ] || [ -n "${STARSHIP_SHELL:-}" ] || [ -n "${STARSHIP_SESSION_KEY:-}" ] || [ -n "${ITERM_SHELL_INTEGRATION_INSTALLED:-}" ]; then
  printf "\033]1337;NetcattyHookSkip;bash\007"
  return 0
fi
if declare -F atuin_preexec >/dev/null 2>&1 || declare -F iterm2_preexec >/dev/null 2>&1 || declare -F __iterm2_prompt_mark >/dev/null 2>&1; then
  printf "\033]1337;NetcattyHookSkip;bash\007"
  return 0
fi
case $- in
  *i*) ;;
  *)
    printf "\033]1337;NetcattyHookSkip;bash\007"
    return 0
    ;;
esac

# The hook is only useful when it can encode the private sequence. Without
# base64/tr it must install nothing, including the prompt handshake, so the
# tap keeps using native OSC or typed-input capture instead of suppressing
# the fallback for a hook that can never report a command. The || true keeps
# set -e in a user rc from exiting an interactive shell on the failed probe.
__netcatty_encoder_probe=$(printf x 2>/dev/null | command base64 2>/dev/null | command tr -d "\n" 2>/dev/null | command tr "+/" "-_" 2>/dev/null | command tr -d "=" 2>/dev/null) || true
if [ -z "$__netcatty_encoder_probe" ]; then
  printf "\033]1337;NetcattyHookSkip;bash\007"
  return 0
fi

__netcatty_b64() {
  printf "%s" "$1" | command base64 2>/dev/null | command tr -d "\n" | command tr "+/" "-_" | command tr -d "="
}

__netcatty_read_history() {
  # history prints "  <number>  <command>"; embedded newlines in a multiline
  # entry are kept. The number only advances when bash added this command.
  local __nc_rest
  __netcatty_hist_number=
  __netcatty_hist_command=
  __nc_rest=$(HISTTIMEFORMAT= builtin history 1 2>/dev/null) || return 0
  while [ "${__nc_rest:0:1}" = " " ]; do
    __nc_rest=${__nc_rest:1}
  done
  while [ -n "$__nc_rest" ]; do
    case "${__nc_rest:0:1}" in
      [0-9]) __netcatty_hist_number=$__netcatty_hist_number${__nc_rest:0:1}; __nc_rest=${__nc_rest:1} ;;
      *) break ;;
    esac
  done
  while [ "${__nc_rest:0:1}" = " " ]; do
    __nc_rest=${__nc_rest:1}
  done
  __netcatty_hist_command=$__nc_rest
}

__netcatty_preexec() {
  local __nc_line=${1-}
  if [ -z "$__nc_line" ]; then
    if [ "${__netcatty_ready:-0}" != "1" ]; then
      return 0
    fi
    __netcatty_ready=0
    __netcatty_read_history
    if [ -n "$__netcatty_hist_number" ] &&
      [ "$__netcatty_hist_number" != "${__netcatty_hist_at_prompt:-}" ]; then
      __nc_line=$__netcatty_hist_command
    else
      # HISTCONTROL (a leading space) or HISTIGNORE kept the command out of
      # history, so history 1 is still the previous command. BASH_COMMAND is
      # the command bash is about to run; it is exact for one logical command,
      # including a multiline one. A hidden compound command such as a line
      # starting with a space followed by echo one; echo two is reported as its
      # first simple command: the DEBUG trap fires per simple command, and bash
      # exposes the complete suppressed line nowhere else. Reconstructing it by
      # joining BASH_COMMAND values would invent separators and change the
      # meaning of and-or lists and pipelines, so the first command is recorded
      # rather than a fabricated whole.
      __nc_line=${BASH_COMMAND:-}
    fi
  fi
  if [ -z "$__nc_line" ]; then
    return 0
  fi
  local __nc_command __nc_cwd
  __nc_command=$(__netcatty_b64 "$__nc_line")
  __nc_cwd=$(__netcatty_b64 "$PWD")
  if [ -n "$__nc_command" ] && [ -n "$__nc_cwd" ]; then
    printf "\033]1337;NetcattyExec;bash;%s;%s\007" "$__nc_cwd" "$__nc_command"
  fi
  printf "\033]133;C\007"
}

__netcatty_save_status() {
  __netcatty_status=$?
  # The prompt cycle starts here. Clearing the flag first means pressing Enter
  # on an empty line, which runs no command and so fires no DEBUG trap, can
  # never replay the previous command at the next prompt.
  __netcatty_ready=0
}

__netcatty_precmd() {
  printf "\033]133;D;%s\007" "${__netcatty_status:-0}"
  printf "\033]1337;NetcattyHook;bash\007"
  printf "\033]133;A\007"
  printf "\033]7;file://%s%s\007" "${HOSTNAME:-}" "$PWD"
  printf "\033]133;B\007"
  __netcatty_read_history
  __netcatty_hist_at_prompt=$__netcatty_hist_number
  __netcatty_ready=1
}

__netcatty_ready=0
__netcatty_hist_at_prompt=
if declare -p preexec_functions >/dev/null 2>&1 && declare -p precmd_functions >/dev/null 2>&1; then
  preexec_functions+=(__netcatty_preexec)
  precmd_functions=(__netcatty_save_status "${precmd_functions[@]}" __netcatty_precmd)
else
  __netcatty_prev_debug=$(trap -p DEBUG 2>/dev/null)
  if [ -n "$__netcatty_prev_debug" ]; then
    __netcatty_prev_debug=${__netcatty_prev_debug#trap -- }
    __netcatty_prev_debug=${__netcatty_prev_debug% DEBUG}
    eval "__netcatty_prev_debug=$__netcatty_prev_debug"
  fi
  __netcatty_debug() {
    local __nc_debug_status=$?
    if [ "${BASH_COMMAND:-}" != "__netcatty_save_status" ] && [ "${__netcatty_ready:-0}" = "1" ]; then
      __netcatty_preexec
    fi
    if [ -n "${__netcatty_prev_debug:-}" ]; then
      eval "$__netcatty_prev_debug"
    fi
    return $__nc_debug_status
  }
  trap "__netcatty_debug" DEBUG
  __netcatty_prev_prompt=${PROMPT_COMMAND-}
  while [ "${__netcatty_prev_prompt%[; ]}" != "$__netcatty_prev_prompt" ]; do
    __netcatty_prev_prompt=${__netcatty_prev_prompt%[; ]}
  done
  case "$(declare -p PROMPT_COMMAND 2>/dev/null)" in
    "declare -a"*)
      PROMPT_COMMAND=(__netcatty_save_status "${PROMPT_COMMAND[@]}" __netcatty_precmd)
      ;;
    *)
      PROMPT_COMMAND="__netcatty_save_status;${__netcatty_prev_prompt:+ $__netcatty_prev_prompt;} __netcatty_precmd"
      ;;
  esac
fi
export NETCATTY_HOOK=1
`

// fishHookText adds fish event handlers. Adding a second handler for
// fish_preexec, fish_postexec, and fish_prompt preserves the user's handlers;
// fish sources the user's config before --init-command runs.
const fishHookText = `if set -q fish_private_mode
    printf "\e]1337;NetcattyHookSkip;fish\a"
    return
end

if set -q NETCATTY_HOOK
    if not functions -q __netcatty_precmd
        printf "\e]1337;NetcattyHookSkip;fish\a"
    end
    return
end

if set -q ATUIN_SESSION; or set -q STARSHIP_SHELL; or set -q STARSHIP_SESSION_KEY; or set -q ITERM_SHELL_INTEGRATION_INSTALLED
    printf "\e]1337;NetcattyHookSkip;fish\a"
    return
end

if functions -q atuin_preexec; or functions -q iterm2_preexec
    printf "\e]1337;NetcattyHookSkip;fish\a"
    return
end

if not status is-interactive
    printf "\e]1337;NetcattyHookSkip;fish\a"
    return
end

# Without base64/tr the private sequence cannot be encoded, so install
# nothing (including the prompt handshake) and leave Tier 2/3 fallback intact.
set -l __netcatty_encoder_probe (printf x | base64 2>/dev/null | tr -d "\n" | tr "+/" "-_" | tr -d "=")
if test -z "$__netcatty_encoder_probe"
    printf "\e]1337;NetcattyHookSkip;fish\a"
    return
end

function __netcatty_b64
    printf "%s" "$argv" | base64 2>/dev/null | tr -d "\n" | tr "+/" "-_" | tr -d "="
end

function __netcatty_preexec --on-event fish_preexec
    set -l __nc_line "$argv"
    if test -z "$__nc_line"
        set __nc_line (commandline)
    end
    if test -z "$__nc_line"
        return
    end
    set -l __nc_command (__netcatty_b64 "$__nc_line")
    set -l __nc_cwd (__netcatty_b64 "$PWD")
    if test -n "$__nc_command"; and test -n "$__nc_cwd"
        printf "\e]1337;NetcattyExec;fish;%s;%s\a" "$__nc_cwd" "$__nc_command"
    end
    printf "\e]133;C\a"
end

function __netcatty_postexec --on-event fish_postexec
    printf "\e]133;D;%s\a" "$status"
end

function __netcatty_prompt --on-event fish_prompt
    printf "\e]1337;NetcattyHook;fish\a"
    printf "\e]133;A\a"
    printf "\e]7;file://%s%s\a" "$hostname" "$PWD"
    printf "\e]133;B\a"
end

set -gx NETCATTY_HOOK 1
`

// nuHookText appends to Nushell's pre_execution and pre_prompt hooks. Nushell
// loads the user config before -e runs this file, so the user's own hooks stay
// installed and run first. The pre_execution hook has the exact command line
// and cwd; the pre_prompt hook has LAST_EXIT_CODE. Nushell also emits native
// OSC 133/7, so the private sequence upgrades an already usable Tier 2
// fallback to Tier 1 instead of replacing a working integration.
const nuHookText = `# Netcatty session hook for Nushell. It appends to the user hooks and never
# replaces them. It emits the private NetcattyExec sequence plus OSC 133/7.

def __netcatty_b64 [value: string] {
  $value | encode base64 --url --nopad
}

def __netcatty_skip [] {
  print -n $"\u{1b}]1337;NetcattyHookSkip;nu\u{7}"
}

if ($env.NETCATTY_HOOK? | default "") != "" {
  __netcatty_skip
} else if ($env.ATUIN_SESSION? | default "") != "" {
  __netcatty_skip
} else if ($env.STARSHIP_SHELL? | default "") != "" {
  __netcatty_skip
} else if ($env.STARSHIP_SESSION_KEY? | default "") != "" {
  __netcatty_skip
} else if ($env.ITERM_SHELL_INTEGRATION_INSTALLED? | default "") != "" {
  __netcatty_skip
} else {
  let __netcatty_ready = (try {
    let __netcatty_probe = ("x" | encode base64 --url --nopad)
    let __netcatty_pre = ($env.config.hooks.pre_execution | length)
    let __netcatty_prompt = ($env.config.hooks.pre_prompt | length)
    $__netcatty_probe == "eA"
  } catch { false })
  if not $__netcatty_ready {
    __netcatty_skip
  } else {
    $env.config.hooks.pre_execution = ($env.config.hooks.pre_execution | append {||
      let __netcatty_line = (commandline)
      if not ($__netcatty_line | is-empty) {
        let __netcatty_cwd = ($env.PWD | into string)
        let __netcatty_enc_cwd = (__netcatty_b64 $__netcatty_cwd)
        let __netcatty_enc_line = (__netcatty_b64 $__netcatty_line)
        print -n $"\u{1b}]1337;NetcattyExec;nu;($__netcatty_enc_cwd);($__netcatty_enc_line)\u{7}"
        print -n $"\u{1b}]133;C\u{7}"
        $env.NETCATTY_NU_PENDING = 1
      }
    })
    $env.config.hooks.pre_prompt = ($env.config.hooks.pre_prompt | append {||
      let __netcatty_pending = ($env.NETCATTY_NU_PENDING? | default 0)
      if $__netcatty_pending == 1 {
        let __netcatty_code = ($env.LAST_EXIT_CODE?)
        if $__netcatty_code == null {
          # An absent LAST_EXIT_CODE is an unknown exit, not a successful 0.
          print -n $"\u{1b}]133;D\u{7}"
        } else {
          print -n $"\u{1b}]133;D;($__netcatty_code)\u{7}"
        }
        $env.NETCATTY_NU_PENDING = 0
      }
      print -n $"\u{1b}]1337;NetcattyHook;nu\u{7}"
      print -n $"\u{1b}]133;A\u{7}"
      let __netcatty_host = ($env.HOSTNAME? | default "")
      let __netcatty_cwd = ($env.PWD | into string)
      print -n $"\u{1b}]7;file://($__netcatty_host)($__netcatty_cwd)\u{7}"
      print -n $"\u{1b}]133;B\u{7}"
    })
    $env.NETCATTY_HOOK = 1
  }
}
`

// xonshHookText appends two event handlers through the xonsh events API.
// Xonsh calls on_precommand with the final command and on_postcommand with the
// return code, so the hook reports the exact command, cwd, exit, and finish
// time. It is installed from a temporary rc.d directory appended to XONSHRC_DIR,
// after every user startup file (including user rc.d files) has run.
const xonshHookText = `# Netcatty session hook for xonsh. It appends event handlers and never
# replaces the user handlers. It emits the private NetcattyExec sequence plus
# OSC 133/7.

import base64 as __netcatty_base64
import os as __netcatty_os
import sys as __netcatty_sys

def __netcatty_b64(value):
    return __netcatty_base64.urlsafe_b64encode(value.encode("utf-8", "replace")).decode("ascii").rstrip("=")

def __netcatty_emit(payload):
    __netcatty_sys.stdout.write(payload)
    __netcatty_sys.stdout.flush()

def __netcatty_skip():
    __netcatty_emit("\x1b]1337;NetcattyHookSkip;xonsh\x07")

def __netcatty_install():
    from xonsh.events import events
    state = {"pending": False}

    @events.on_precommand
    def __netcatty_preexec(cmd=None, **kwargs):
        line = cmd if cmd is not None else kwargs.get("cmd")
        if line is None:
            return
        # The precommand source ends in a newline; keep only the logical line.
        line = line.rstrip("\n")
        if not line:
            return
        try:
            cwd = __netcatty_os.getcwd()
        except OSError:
            cwd = ""
        __netcatty_emit("\x1b]1337;NetcattyExec;xonsh;" + __netcatty_b64(cwd) + ";" + __netcatty_b64(line) + "\x07")
        __netcatty_emit("\x1b]133;C\x07")
        state["pending"] = True

    @events.on_postcommand
    def __netcatty_postexec(rtn=None, **kwargs):
        if state["pending"]:
            state["pending"] = False
            if isinstance(rtn, int):
                __netcatty_emit("\x1b]133;D;" + str(rtn) + "\x07")
            else:
                __netcatty_emit("\x1b]133;D\x07")
        __netcatty_emit("\x1b]1337;NetcattyHook;xonsh\x07")
        __netcatty_emit("\x1b]133;A\x07")
        try:
            cwd = __netcatty_os.getcwd()
        except OSError:
            cwd = ""
        host = __netcatty_os.environ.get("HOSTNAME", "")
        __netcatty_emit("\x1b]7;file://" + host + cwd + "\x07")
        __netcatty_emit("\x1b]133;B\x07")

    $NETCATTY_HOOK = "1"

if __netcatty_os.environ.get("NETCATTY_HOOK"):
    __netcatty_skip()
elif any(__netcatty_os.environ.get(name) for name in ("ATUIN_SESSION", "STARSHIP_SHELL", "STARSHIP_SESSION_KEY", "ITERM_SHELL_INTEGRATION_INSTALLED")):
    __netcatty_skip()
elif not bool($XONSH_INTERACTIVE):
    __netcatty_skip()
else:
    try:
        __netcatty_install()
    except Exception:
        __netcatty_skip()
`

// writeZshStartup writes the five zsh startup files. The user's own files are
// only sourced; nothing is created in their home directory.
func writeZshStartup(dir string) error {
	files := []struct {
		name string
		body string
	}{
		{".zshenv", zshEnvScript},
		{".zprofile", zshSourceScript(".zprofile")},
		{".zshrc", zshRC()},
		{".zlogin", zshSourceScript(".zlogin")},
		{".zlogout", zshSourceScript(".zlogout")},
	}
	for _, file := range files {
		if err := writePrivateFile(dir, file.name, file.body); err != nil {
			return err
		}
	}
	return nil
}

// remoteWrapperCommand is the SSH RemoteCommand. It runs a POSIX shell
// explicitly, so the login shell only has to parse one simple command.
func remoteWrapperCommand() string {
	return "exec /bin/sh -c " + quotePOSIX(remoteWrapperScript())
}

// remoteWrapperScript detects the login shell, creates a private temporary
// directory, writes the session startup files there, runs the shell, and
// removes the directory when the shell exits.
func remoteWrapperScript() string {
	var builder strings.Builder
	builder.WriteString(remotePrelude)
	builder.WriteString("case \"$netcatty_name\" in\n")
	builder.WriteString("zsh)\n")
	builder.WriteString(remoteFile(".zshenv", zshEnvScript))
	builder.WriteString(remoteFile(".zprofile", zshSourceScript(".zprofile")))
	builder.WriteString(remoteFile(".zshrc", zshRC()))
	builder.WriteString(remoteFile(".zlogin", zshSourceScript(".zlogin")))
	builder.WriteString(remoteFile(".zlogout", zshSourceScript(".zlogout")))
	builder.WriteString(`netcatty_zdotdir=${ZDOTDIR:-$HOME}
NETCATTY_USER_ZDOTDIR="$netcatty_zdotdir" NETCATTY_HOOK_DIR="$netcatty_dir" ZDOTDIR="$netcatty_dir" "$netcatty_shell" -l
` + ";;\n")
	builder.WriteString("bash)\n")
	builder.WriteString(remoteFile("bashrc", bashStartupScript()))
	builder.WriteString(`NETCATTY_HOOK_DIR="$netcatty_dir" "$netcatty_shell" --rcfile "$netcatty_dir/bashrc" -i
` + ";;\n")
	builder.WriteString("fish)\n")
	builder.WriteString(remoteFile("hook.fish", fishHookText))
	builder.WriteString(`NETCATTY_HOOK_FILE="$netcatty_dir/hook.fish" "$netcatty_shell" -l --init-command "source \"\$NETCATTY_HOOK_FILE\""
` + ";;\n")
	builder.WriteString("nu)\n")
	builder.WriteString(remoteFile("hook.nu", nuHookText))
	builder.WriteString(`NETCATTY_HOOK_DIR="$netcatty_dir" "$netcatty_shell" -l -e "source \"$netcatty_dir/hook.nu\""
` + ";;\n")
	builder.WriteString("xonsh)\n")
	builder.WriteString(remoteFile("hook.xsh", xonshHookText))
	builder.WriteString(`NETCATTY_HOOK_DIR="$netcatty_dir"
if [ -n "${XONSHRC_DIR:-}" ]; then
  XONSHRC_DIR="$XONSHRC_DIR:$netcatty_dir"
else
  XONSHRC_DIR="${XONSH_SYS_CONFIG_DIR:-/etc/xonsh}/rc.d:${XONSH_CONFIG_DIR:-${XDG_CONFIG_HOME:-${HOME}/.config}/xonsh}/rc.d:$netcatty_dir"
fi
export XONSHRC_DIR NETCATTY_HOOK_DIR
"$netcatty_shell" -l
` + ";;\n")
	builder.WriteString("esac\n")
	builder.WriteString("netcatty_status=$?\nexit $netcatty_status\n")
	return builder.String()
}

const remotePrelude = `netcatty_shell=${SHELL:-}
if [ -z "$netcatty_shell" ] || [ ! -x "$netcatty_shell" ]; then
  netcatty_shell=$(getent passwd "$(id -u)" 2>/dev/null | cut -d: -f7)
fi
if [ -z "$netcatty_shell" ] || [ ! -x "$netcatty_shell" ]; then
  # BSD and macOS have no getent, so read the login shell from the passwd file.
  netcatty_uid=$(id -u)
  while IFS=: read -r netcatty_pw_name netcatty_pw_pw netcatty_pw_uid netcatty_pw_gid netcatty_pw_gecos netcatty_pw_home netcatty_pw_shell; do
    if [ "$netcatty_pw_uid" = "$netcatty_uid" ]; then
      netcatty_shell=$netcatty_pw_shell
      break
    fi
  done < /etc/passwd 2>/dev/null
fi
if [ -z "$netcatty_shell" ] || [ ! -x "$netcatty_shell" ]; then
  netcatty_shell=/bin/sh
fi
netcatty_name=${netcatty_shell##*/}
case "$netcatty_name" in
zsh|bash|fish|nu|xonsh)
  ;;
*)
  printf "\033]1337;NetcattyHookSkip\007"
  exec "$netcatty_shell" -l
  ;;
esac
netcatty_dir=$(mktemp -d "${TMPDIR:-/tmp}/netcatty.XXXXXX") || {
  printf "\033]1337;NetcattyHookSkip\007"
  exec "$netcatty_shell" -l
}
netcatty_keep=1
cleanup() {
  if [ -n "$netcatty_keep" ]; then
    netcatty_keep=
    rm -rf "$netcatty_dir"
  fi
}
trap cleanup EXIT
trap ":" INT QUIT
trap "cleanup; exit 129" HUP
trap "cleanup; exit 143" TERM
`

// remoteFile writes one heredoc. Quoting the delimiter keeps every shell
// expansion in the body literal.
func remoteFile(name, body string) string {
	return "cat > \"$netcatty_dir/" + name + "\" <<\"NETCATTY_EOF\"\n" + body + "NETCATTY_EOF\n"
}

// quotePOSIX turns a value into one POSIX shell word.
func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// writePrivateFile writes one session file readable only by the user.
func writePrivateFile(dir, name, body string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
}
