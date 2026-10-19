package state

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestTabSummariesFollowTabsAndDetachedOrder(t *testing.T) {
	graph := newTestGraph(t)
	if summaries := graph.state.TabSummaries(); len(summaries) != 0 {
		t.Fatalf("empty state summaries = %+v", summaries)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	summaries := graph.state.TabSummaries()
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
	summary := summaries[0]
	if summary.TabIndex != 0 || summary.Detached || summary.TabID == "" || summary.Number != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	switch summary.Status {
	case TabConnected, TabConnecting, TabDisconnected:
	default:
		t.Fatalf("status = %q", summary.Status)
	}

	if len(graph.sessions) != 1 {
		t.Fatalf("sessions = %d", len(graph.sessions))
	}
	label, ok := graph.state.SessionLabel(graph.sessions[0].ID())
	if !ok || label != summary.Label {
		t.Fatalf("SessionLabel = %q, %v; want %q", label, ok, summary.Label)
	}
	if _, ok := graph.state.SessionLabel(model.SessionID("missing")); ok {
		t.Fatal("an unknown Session produced a label")
	}

	if !graph.state.DetachTab(0) {
		t.Fatal("DetachTab failed")
	}
	summaries = graph.state.TabSummaries()
	if len(summaries) != 1 || !summaries[0].Detached || summaries[0].TabIndex != -1 {
		t.Fatalf("detached summaries = %+v", summaries)
	}
}

func TestAggregateStatusWithoutSessionsIsConnecting(t *testing.T) {
	tab := &Tab{panes: map[model.PaneID]*pane{"p": {}}}
	if status := aggregateStatus(tab); status != TabConnecting {
		t.Fatalf("status = %q", status)
	}
	tab = &Tab{panes: map[model.PaneID]*pane{}}
	if status := aggregateStatus(tab); status != TabConnecting {
		t.Fatalf("empty status = %q", status)
	}
}
