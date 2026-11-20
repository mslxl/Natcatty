package integration

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PowerShellScript is the script the terminal runs with
// `powershell -NoLogo -NoExit -Command <script>`, after the user's profiles
// have been read. It emits Terminal's marks with the Windows PowerShell 5.1 and
// PowerShell 7 hosts:
//
//	OSC 133;A            the prompt is starting        (from `prompt`)
//	OSC 133;B            the prompt has been printed   (from `prompt`)
//	OSC 133;C;<typed>;<expanded>  a command is starting (Enter key handler;
//	                     both fields are the typed line, url-escaped, since
//	                     PowerShell reports no alias-expanded line)
//	OSC 133;D;<status>   the command finished          (from `prompt`)
//	OSC 7;file://host/path  the working directory      (from `prompt`)
//
// It contains no double quotes, so the Windows command line needs no
// escaping beyond the standard quoting.
func PowerShellScript() string {
	return strings.Join([]string{
		`$global:__natcattyPending=$false`,
		// A confined session (no write access to %APPDATA%) makes PSReadLine
		// print "Error reading or writing history file" on the first command,
		// which lands inside that command's block. Turn history saving off in
		// that case instead of letting it shout: the user cannot save it
		// anyway.
		`if(Get-Command Set-PSReadLineOption -ErrorAction SilentlyContinue){`,
		`try{`,
		`$__natcattyHist=(Get-PSReadLineOption).HistorySavePath`,
		`$__natcattyOk=$false`,
		`if($__natcattyHist){`,
		`$__natcattyDir=Split-Path -Parent $__natcattyHist`,
		`if($__natcattyDir -and -not (Test-Path $__natcattyDir)){New-Item -ItemType Directory -Force -Path $__natcattyDir -ErrorAction Stop | Out-Null}`,
		`$__natcattyStream=[IO.File]::Open($__natcattyHist,'Append','Write')`,
		`$__natcattyStream.Close()`,
		`$__natcattyOk=$true`,
		`}`,
		`if(-not $__natcattyOk){Set-PSReadLineOption -HistorySaveStyle SaveNothing -ErrorAction SilentlyContinue}`,
		`}catch{`,
		`Set-PSReadLineOption -HistorySaveStyle SaveNothing -ErrorAction SilentlyContinue`,
		`}`,
		`}`,
		`function global:prompt{`,
		`$__natcattyOk=$?`,
		`if($global:__natcattyPending){`,
		`$c=0`,
		`if(-not $__natcattyOk){`,
		`if($global:LASTEXITCODE -is [int] -and $global:LASTEXITCODE -ne 0){$c=$global:LASTEXITCODE}else{$c=1}`,
		`}`,
		`[Console]::Write([char]27+']133;D;'+$c+[char]7)`,
		`$global:__natcattyPending=$false`,
		`}`,
		`$__natcattyPath=(Get-Location).Path`,
		`$d=$__natcattyPath.Replace('\','/')`,
		`[Console]::Write([char]27+']133;A'+[char]7)`,
		`[Console]::Write([char]27+']7;file://'+$env:COMPUTERNAME+'/'+$d+[char]7)`,
		`$p='PS '+$__natcattyPath+'> '`,
		`[Console]::Write([char]27+']133;B'+[char]7)`,
		`return $p`,
		`}`,
		// Terminal opens the same native view for an alias as for its command
		// ("Aliases such as gst open the same native views"), so the typed line
		// is sent twice: as typed, and alias-expanded.
		`function global:__natcattyExpand([string]$line){`,
		`try{`,
		`$m=[regex]::Match($line,'^(\s*)([^\s]+)(.*)$')`,
		`if(-not $m.Success){return $line}`,
		`$a=Get-Alias -Name $m.Groups[2].Value -ErrorAction SilentlyContinue`,
		`if($a -and $a.Definition){return $m.Groups[1].Value+$a.Definition+$m.Groups[3].Value}`,
		`}catch{}`,
		`return $line`,
		`}`,
		`if(Get-Command Set-PSReadLineKeyHandler -ErrorAction SilentlyContinue){`,
		`Set-PSReadLineKeyHandler -Key Enter -ScriptBlock{`,
		`$__natcattyLine=$null`,
		`$__natcattyCursor=$null`,
		`[Microsoft.PowerShell.PSConsoleReadLine]::GetBufferState([ref]$__natcattyLine,[ref]$__natcattyCursor)`,
		`$__natcattyEnc=[uri]::EscapeDataString($__natcattyLine)`,
		`$__natcattyEncExpanded=[uri]::EscapeDataString((__natcattyExpand $__natcattyLine))`,
		`[Console]::Write([char]27+']133;C;'+$__natcattyEnc+';'+$__natcattyEncExpanded+[char]7)`,
		`$global:__natcattyPending=$true`,
		`[Microsoft.PowerShell.PSConsoleReadLine]::AcceptLine()`,
		`}`,
		`}`,
	}, "\n")
}

// BashScript is sourced with `bash --rcfile <file>`; it reads the user's own
// startup files itself, as a login shell would.
func BashScript() string {
	return `# Terminal-style shell integration for bash, installed by natcatty.
# Sourced with --rcfile, so the user's own startup files are read here.
if shopt -q login_shell; then
  [ -f /etc/profile ] && . /etc/profile
  for __natcatty_f in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
    if [ -f "$__natcatty_f" ]; then . "$__natcatty_f"; break; fi
  done
  unset __natcatty_f
else
  [ -f "$HOME/.bashrc" ] && . "$HOME/.bashrc"
fi

__natcatty_urlencode() {
  local s=$1 out= c i
  for ((i=0;i<${#s};i++)); do
    c=${s:i:1}
    case $c in
      [a-zA-Z0-9.~_-]) out+=$c ;;
      *) out+=$(printf '%%%02X' "'$c") ;;
    esac
  done
  printf %s "$out"
}

__natcatty_busy=
__natcatty_preexec() {
  [ -n "$__natcatty_busy" ] && return 0
  __natcatty_busy=1
  local line enc
  line=$(HISTTIMEFORMAT= builtin history 1)
  line=${line#*[0-9]  }
  [ -n "$line" ] || return 0
  enc=$(__natcatty_urlencode "$line")
  printf '\033]133;C;%s;%s\007' "$enc" "$enc"
}

__natcatty_prompt() {
  local code=$?
  __natcatty_busy=
  printf '\033]133;D;%s\007' "$code"
  printf '\033]133;A\007'
  printf '\033]7;file://%s%s\007' "${HOSTNAME:-localhost}" "$PWD"
  printf '\033]133;B\007'
  # The user's own precmd runs after this one and reads $? to report the
  # command's status (starship's status module does), so hand it back.
  return "$code"
}

# The user's startup files, read above, may already own PROMPT_COMMAND and the
# DEBUG trap: starship keeps its precmd in PROMPT_COMMAND and its timing in the
# DEBUG trap, and bash-preexec owns the DEBUG trap too. Assigning either one
# froze those prompts and silenced those hooks, so the integration only layers
# on top: its precmd runs first, with the status of the command that just
# finished, then the user's hooks run as they did before.
case ${PROMPT_COMMAND-} in
  *__natcatty_prompt*) ;;
  '') PROMPT_COMMAND=__natcatty_prompt ;;
  *)
    # bash 5.1 and later run every element of an array PROMPT_COMMAND; earlier
    # bashes treat the value as one command line.
    if [ "${BASH_VERSINFO[0]}" -gt 5 ] || { [ "${BASH_VERSINFO[0]}" -eq 5 ] && [ "${BASH_VERSINFO[1]}" -ge 1 ]; }; then
      PROMPT_COMMAND=(__natcatty_prompt "${PROMPT_COMMAND[@]}")
    else
      PROMPT_COMMAND="__natcatty_prompt;$PROMPT_COMMAND"
    fi
    ;;
esac

# trap -p prints 'trap -- <command> DEBUG'; the command is word 2 of that list.
# bash refuses to re-enter a trap while it runs, so chaining cannot recurse.
eval "__natcatty_debug_trap=($(trap -p DEBUG))"
__natcatty_debug_trap=${__natcatty_debug_trap[2]-}
if [ -z "$__natcatty_debug_trap" ]; then
  trap '__natcatty_preexec' DEBUG
elif [ "$__natcatty_debug_trap" != '__natcatty_preexec' ]; then
  __natcatty_preexec_all() {
    local __natcatty_last=$1
    eval -- "$__natcatty_debug_trap"
    __natcatty_preexec
    : "$__natcatty_last"
  }
  trap '__natcatty_preexec_all "$_"' DEBUG
fi
`
}

// ZshScript is read as $ZDOTDIR/.zshenv; the user's own ZDOTDIR (which
// Shell passes as KITTY_ZDOTDIR) is restored and read first.
func ZshScript() string {
	return `# Terminal-style shell integration for zsh, installed by natcatty.
# zsh reads this file as $ZDOTDIR/.zshenv; the user's own files are read
# from KITTY_ZDOTDIR, which the terminal set to their directory.
__natcatty_zdotdir=${KITTY_ZDOTDIR:-$HOME}
export ZDOTDIR=$__natcatty_zdotdir
[ -f "$__natcatty_zdotdir/.zshenv" ] && source "$__natcatty_zdotdir/.zshenv"
[[ -o interactive ]] || return 0

__natcatty_urlencode() {
  local s=$1 out= c i
  for (( i=1; i<=${#s}; i++ )); do
    c=$s[i]
    case $c in
      [a-zA-Z0-9.~_-]) out+=$c ;;
      *) out+=$(printf '%%%02X' "'$c") ;;
    esac
  done
  print -rn -- "$out"
}

__natcatty_preexec() {
  local enc
  enc=$(__natcatty_urlencode "$1")
  print -n -- $'\e]133;C;'$enc';'$enc$'\a'
}

__natcatty_precmd() {
  local code=$?
  print -n -- $'\e]133;D;'$code$'\a'
  print -n -- $'\e]133;A\a'
  print -n -- $'\e]7;file://'${HOST:-localhost}$PWD$'\a'
  print -n -- $'\e]133;B\a'
}

autoload -Uz add-zsh-hook
add-zsh-hook preexec __natcatty_preexec
add-zsh-hook precmd __natcatty_precmd
`
}

// FishScript is sourced with `fish -C 'source <file>'`.
func FishScript() string {
	return `# Terminal-style shell integration for fish, installed by natcatty.
function __natcatty_urlencode
    string escape --style=url -- $argv[1]
end

function __natcatty_preexec --on-event fish_preexec
    set -l enc (__natcatty_urlencode $argv[1])
    printf '\e]133;C;%s;%s\a' $enc $enc
end

function __natcatty_postexec --on-event fish_postexec
    printf '\e]133;D;%s\a' $status
end

function __natcatty_prompt --on-event fish_prompt
    printf '\e]133;A\a'
    printf '\e]7;file://%s%s\a' (hostname) $PWD
    printf '\e]133;B\a'
end
`
}

// POSIXScript is read from $ENV by interactive POSIX shells. POSIX sh has no
// pre-execution hook, so only the prompt marks (A, B, D and OSC 7) are
// emitted: no 133;C, and the lenses see no command start.
func POSIXScript() string {
	return `# Terminal-style shell integration for POSIX sh, installed by natcatty.
__natcatty_prompt() {
  printf '\033]133;D;%s\007' "$?"
  printf '\033]133;A\007'
  printf '\033]7;file://%s%s\007' "${HOSTNAME:-localhost}" "$PWD"
  printf '\033]133;B\007'
}
PS1='$(__natcatty_prompt)$ '
`
}

var (
	scriptOnce sync.Once
	scriptRoot string
	scriptErr  error
)

// scriptDir is where the generated scripts are written, once per process.
// They live in the system's temporary directory and stay for the life of the
// process, since the shells read them while they run.
func scriptDir() string {
	scriptOnce.Do(func() {
		dir, err := os.MkdirTemp("", "natcatty-integration-")
		if err != nil {
			scriptErr = err
			return
		}
		scriptRoot = dir
	})
	if scriptErr != nil {
		return ""
	}
	return scriptRoot
}

// writeScript writes one generated script and returns its path, or "" when it
// cannot be written (the caller then starts a plain shell).
func writeScript(name, content string) string {
	dir := scriptDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return ""
	}
	return path
}

// zshZDOTDIR writes zsh/.zshenv under the script directory and returns the
// directory, for ZDOTDIR.
func zshZDOTDIR() string {
	dir := scriptDir()
	if dir == "" {
		return ""
	}
	zdot := filepath.Join(dir, "zsh")
	if err := os.MkdirAll(zdot, 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(filepath.Join(zdot, ".zshenv"), []byte(ZshScript()), 0o600); err != nil {
		return ""
	}
	return zdot
}
