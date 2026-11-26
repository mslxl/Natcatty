// Package app shares command-capture wiring between the desktop and phone
// composition roots. Everything in this file is platform-neutral: it decides
// how one completed Session command becomes a journal row, and nothing here
// reaches a desktop-only integration.
package app

import (
	"os"
	"os/user"
	"time"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// commandLocalIdentity is this machine's origin identity for a local shell.
// Empty parts let the fallback rule choose the recorded Host label or
// "unknown" rather than inventing a value.
type commandLocalIdentity struct {
	Host string
	User string
}

// resolveCommandLocalIdentity reads the local OS hostname and username once
// when capture is wired. A failure leaves that part empty.
func resolveCommandLocalIdentity() commandLocalIdentity {
	host, _ := os.Hostname()
	name := ""
	if current, err := user.Current(); err == nil {
		name = current.Username
	}
	return commandLocalIdentity{Host: host, User: name}
}

// commandCompletionHook turns one completed Session command into exactly one
// journal row. The row goes through autocomplete.RecordEntry, which is the
// existing single write path to the history journal. A transient completion,
// which is every command in a Host-Atuin Session, is kept only in the bounded
// per-Session memory buffer: it is never journaled, creates no record, and is
// never uploaded.
func commandCompletionHook(service *autocomplete.Service,
	local commandLocalIdentity, transient *transientCommands) func(appsession.CommandEvent) {
	return func(event appsession.CommandEvent) {
		if event.Transient {
			if transient != nil {
				transient.Remember(event.SessionID, commandEntryFromEvent(event, local))
			}
			return
		}
		if service == nil {
			return
		}
		service.RecordEntry(commandEntryFromEvent(event, local))
	}
}

// commandEntryFromEvent maps a completion event to the persisted history row.
// The app resolves the Host context, scope, origin, and author here; the tap
// only captured what it observed. Origin is captured now and never rewritten,
// and a serial Session leaves it empty because a serial port is not a host.
func commandEntryFromEvent(event appsession.CommandEvent, local commandLocalIdentity) domainhistory.Entry {
	// An empty Kind is a local shell, matching Service.Open's normalization.
	isLocal := event.Kind == appsession.KindLocal || event.Kind == ""
	unsaved := event.HostID == "" && !isLocal && event.Kind != appsession.KindSerial
	origin := commandOrigin(event, local)
	scope := event.Scope
	switch {
	case scope != "":
	case event.HostID != "":
		scope = domainhistory.NormalizeScope(event.HostID)
	case isLocal:
		scope = "local"
	case event.Kind == appsession.KindSerial:
		scope = "serial"
	default:
		// An unsaved Host's scope carries the origin captured for the row, so
		// typed-Host suggestions stay separate from the local shell and saved
		// Hosts.
		scope = "host:unsaved:" + origin
	}
	label := event.HostLabel
	if event.HostID == "" && !unsaved {
		// A local shell and a serial Session keep no Host label; the UI names
		// them from their scope. An unsaved Host keeps the typed label.
		label = ""
	}
	var duration time.Duration
	if !event.FinishedAt.IsZero() && !event.StartedAt.IsZero() {
		duration = event.FinishedAt.Sub(event.StartedAt)
		if duration < 0 {
			duration = 0
		}
	}
	return domainhistory.Entry{
		SessionID:   event.SessionID,
		HostID:      event.HostID,
		HostLabel:   label,
		UnsavedHost: unsaved,
		Scope:       scope,
		Command:     event.Command,
		CWD:         event.CWD,
		Shell:       event.Shell,
		RecordedAt:  event.StartedAt,
		FinishedAt:  event.FinishedAt,
		Duration:    duration,
		ExitCode:    event.ExitCode,
		HasExitCode: event.HasExitCode,
		Origin:      origin,
		Source:      domainhistory.SourceSession,
		CaptureTier: event.CaptureTier,
	}
}

// commandOrigin derives the captured host:user origin. A saved Host uses its
// network hostname and SSH username, an unsaved Host its typed hostname and
// username, and the local shell the OS hostname and username. A missing host
// part falls back to the recorded Host label, then "unknown"; a missing user
// part is "unknown". A serial Session has no origin.
func commandOrigin(event appsession.CommandEvent, local commandLocalIdentity) string {
	if event.Kind == appsession.KindSerial {
		return ""
	}
	host, name := event.Hostname, event.Username
	if event.Kind == appsession.KindLocal {
		host, name = local.Host, local.User
	}
	return domainhistory.CaptureOrigin(host, name, event.HostLabel)
}
