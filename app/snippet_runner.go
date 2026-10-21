package app

import (
	"errors"
	"fmt"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// errNoFocusedSession reports that "Run here" was used without a focused
// Session. The Vault state localizes this case before the runner is called.
var errNoFocusedSession = errors.New("snippet: no focused Session")

// snippetRunner connects the Vault's run actions to the shell and Session
// service. The resolved command is already substituted; this type only turns
// it into the exact bytes each target receives.
type snippetRunner struct {
	shell    func() *state.State
	sessions func() *appsession.Service
}

// HasActiveSession reports whether a Session is focused.
func (r snippetRunner) HasActiveSession() bool {
	shell := r.shell()
	if shell == nil {
		return false
	}
	_, ok := shell.FocusedSessionID()
	return ok
}

// ActiveHostID reports the Host of the focused Session. A local shell reports
// false, so a scoped Snippet cannot run there.
func (r snippetRunner) ActiveHostID() (model.HostID, bool) {
	shell := r.shell()
	if shell == nil {
		return "", false
	}
	return shell.FocusedHostID()
}

// SendToActiveSession writes the resolved command to the focused Session.
func (r snippetRunner) SendToActiveSession(snippet domainvault.Snippet, command string) error {
	shell := r.shell()
	service := r.sessions()
	if shell == nil || service == nil {
		return errNoFocusedSession
	}
	id, ok := shell.FocusedSessionID()
	if !ok {
		return errNoFocusedSession
	}
	preview := snippetPreview(snippet, command)
	if preview.Empty() {
		return nil
	}
	// Send synchronously so a transport failure reaches the run form. Line
	// delay mode spaces writes here rather than hiding a failure in a
	// goroutine; Snippets that use it are short and interactive.
	if result := service.SendChunks([]model.SessionID{id}, preview.Chunks, snippetChunkDelay(snippet)); !result.Complete() {
		return fmt.Errorf("snippet: send to the active Session failed")
	}
	return nil
}

// SendToHosts opens a focused Tab per target Host and sends the resolved
// command once each Session is attached. The original opens a Session for
// every target rather than reusing an unrelated focused Session.
func (r snippetRunner) SendToHosts(hosts []domainvault.Host, snippet domainvault.Snippet, command string) error {
	shell := r.shell()
	if shell == nil {
		return errors.New("snippet: shell is unavailable")
	}
	preview := snippetPreview(snippet, command)
	if preview.Empty() {
		return nil
	}
	startup := state.StartupInput{Chunks: preview.Chunks, Delay: snippetChunkDelay(snippet)}
	for _, host := range hosts {
		if err := shell.OpenHostWithStartup(host, startup); err != nil {
			return err
		}
	}
	return nil
}

// snippetPreview applies the original multi-line behavior: paste by default,
// one write per line when the Snippet asks for line delay, and the trailing
// Enter omitted when NoAutoRun is set.
func snippetPreview(snippet domainvault.Snippet, command string) appsession.ComposePreview {
	mode := appsession.ComposePaste
	if snippet.MultiLineRunMode == domainvault.SnippetRunLineDelay {
		mode = appsession.ComposeLineByLine
	}
	return appsession.TransformCompose(command, appsession.ComposeOptions{
		LineMode:  mode,
		NoAutoRun: snippet.NoAutoRun,
	})
}

func snippetChunkDelay(snippet domainvault.Snippet) time.Duration {
	if snippet.MultiLineRunMode != domainvault.SnippetRunLineDelay {
		return 0
	}
	return appsession.ComposeLineDelay
}
