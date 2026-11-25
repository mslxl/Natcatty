package state

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/motion"
)

// TestMoveTabRecordsTabReorderWithoutABodyWarp is ticket 20's state side: a
// successful reorder records one TabReorder naming the source and target, it
// takes the next sequence, and it raises no body warp (ticket 19's body does
// not move). A rejected move records nothing.
func TestMoveTabRecordsTabReorderWithoutABodyWarp(t *testing.T) {
	graph := newWorkspaceGraph(t)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := graph.state.TabID(0)
	middle, _ := graph.state.TabID(1)
	last, _ := graph.state.TabID(2)
	before, ok := graph.state.MotionEvent()
	if !ok {
		t.Fatalf("the setup recorded no motion event")
	}
	warpBefore := graph.state.BodyWarpSeq()

	if !graph.state.MoveTab(last, first, false) {
		t.Fatalf("MoveTab returned false")
	}
	event, ok := graph.state.MotionEvent()
	if !ok || event.Kind != motion.TabReorder {
		t.Fatalf("the reorder recorded %+v, want one TabReorder", event)
	}
	if event.TabID != last || event.TargetTabID != first {
		t.Fatalf("the reorder event = %+v, want %q before %q", event, last, first)
	}
	if event.Seq <= before.Seq {
		t.Fatalf("the reorder sequence %d does not follow %d", event.Seq, before.Seq)
	}
	if event.At.IsZero() {
		t.Fatalf("the reorder event carries no time")
	}
	if got := graph.state.BodyWarpSeq(); got != warpBefore {
		t.Fatalf("the reorder raised the body warp sequence from %d to %d", warpBefore, got)
	}
	want := []model.TabID{last, first, middle}
	got := idsOf(graph.state)
	if len(got) != len(want) {
		t.Fatalf("tab order = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("tab order = %v, want %v", got, want)
		}
	}

	if graph.state.MoveTab(last, last, false) {
		t.Fatalf("MoveTab accepted a drop onto itself")
	}
	if graph.state.MoveTab(model.TabID("missing"), first, false) {
		t.Fatalf("MoveTab accepted an unknown Tab")
	}
	again, ok := graph.state.MotionEvent()
	if !ok || again.Seq != event.Seq {
		t.Fatalf("a rejected move recorded %+v, want no event after %d", again, event.Seq)
	}
}
