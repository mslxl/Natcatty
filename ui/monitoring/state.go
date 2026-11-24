// Package monitoring renders and drives the desktop Host monitoring surface.
// The state and actions live in application/monitoring, which the phone shell
// also imports; this package adds the desktop table's grid navigation and the
// localized view.
package monitoring

import (
	"time"

	"github.com/egoist/mygo/ui"

	appmonitoring "github.com/mslxl/natcatty/application/monitoring"
)

// The shared application state is the surface's state. These aliases keep the
// desktop view and its tests on the same names while the phone imports the
// application package directly.
type (
	Service        = appmonitoring.Service
	Hosts          = appmonitoring.Hosts
	Preferences    = appmonitoring.Preferences
	OpenTerminal   = appmonitoring.OpenTerminal
	Problem        = appmonitoring.Problem
	Form           = appmonitoring.Form
	Option         = appmonitoring.Option
	PartitionAlert = appmonitoring.PartitionAlert
)

const (
	ProblemNone          = appmonitoring.ProblemNone
	ProblemNameRequired  = appmonitoring.ProblemNameRequired
	ProblemHostRequired  = appmonitoring.ProblemHostRequired
	ProblemDuplicateName = appmonitoring.ProblemDuplicateName
	ProblemSave          = appmonitoring.ProblemSave
	ProblemLoad          = appmonitoring.ProblemLoad
	ProblemTooManyHosts  = appmonitoring.ProblemTooManyHosts
	ProblemNoPanel       = appmonitoring.ProblemNoPanel
	ProblemOpenTerminal  = appmonitoring.ProblemOpenTerminal
	ProblemInternal      = appmonitoring.ProblemInternal

	StatusStopped = appmonitoring.StatusStopped
	StatusStale   = appmonitoring.StatusStale
)

var (
	ColumnKeys   = appmonitoring.ColumnKeys
	RangeChoices = appmonitoring.RangeChoices
)

// IntervalChoices are the refresh cadences the toolbar offers.
func IntervalChoices() []int { return appmonitoring.IntervalChoices() }

// WithRedrawRequest sets the function called when the surface changes.
func WithRedrawRequest(fn func()) Option { return appmonitoring.WithRedrawRequest(fn) }

// WithClock replaces the source of time. Tests use it for stable ages.
func WithClock(now func() time.Time) Option { return appmonitoring.WithClock(now) }

// State is the desktop monitoring surface: the shared application state plus
// the table grid's keyboard focus, which only the desktop list uses.
type State struct {
	*appmonitoring.State
	grid ui.GridState
}

// NewState returns a monitoring surface over its ports.
func NewState(service Service, hosts Hosts, prefs Preferences, open OpenTerminal, opts ...Option) *State {
	s := &State{State: appmonitoring.NewState(service, hosts, prefs, open, opts...)}
	s.grid.Key = func(item int) any {
		hosts := s.DisplayHosts()
		if item >= 0 && item < len(hosts) {
			return string(hosts[item].ID)
		}
		return item
	}
	s.grid.Label = func(item int) string {
		hosts := s.DisplayHosts()
		if item >= 0 && item < len(hosts) {
			return s.HostLabel(hosts[item])
		}
		return ""
	}
	return s
}
