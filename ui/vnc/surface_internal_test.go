package vnc

import (
	"image"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
)

// TestApplyResizeResultRecordsEachOutcome proves the viewer's resize state:
// success clears the requested size, a refusal latches the fallback, and a
// forwarded (pending) status neither latches nor clears the pending request.
func TestApplyResizeResultRecordsEachOutcome(t *testing.T) {
	succeeded := &Surface{resizeInFlight: true, resizeRequested: image.Pt(160, 120)}
	succeeded.applyResizeResult(nil)
	if succeeded.resizeInFlight {
		t.Fatal("success left the resize in flight")
	}
	if succeeded.resizeRequested != (image.Point{}) {
		t.Fatalf("successful size still cached as %v", succeeded.resizeRequested)
	}
	if succeeded.resizeUnsupported {
		t.Fatal("a success latched the local scaling fallback")
	}

	refused := &Surface{resizeInFlight: true, resizeRequested: image.Pt(160, 120)}
	refused.applyResizeResult(appsession.ErrDesktopResizeRefused)
	if refused.resizeInFlight {
		t.Fatal("a refusal left the resize in flight")
	}
	if refused.resizeRequested != (image.Point{}) {
		t.Fatalf("refused size still cached as %v", refused.resizeRequested)
	}
	if !refused.resizeUnsupported {
		t.Fatal("a refusal did not latch the local scaling fallback")
	}

	pending := &Surface{resizeInFlight: true, resizeRequested: image.Pt(160, 120)}
	pending.applyResizeResult(appsession.ErrDesktopResizePending)
	if pending.resizeInFlight {
		t.Fatal("a pending reply left the resize in flight")
	}
	if pending.resizeRequested != image.Pt(160, 120) {
		t.Fatalf("pending size = %v, want the request kept", pending.resizeRequested)
	}
	if pending.resizeUnsupported {
		t.Fatal("a forwarded (status 4) reply latched the local scaling fallback")
	}
}

// TestTimeoutKeepsTheFailedSizeButAllowsADifferentOne proves the per-size
// backoff: after a timeout, redraws at the same pane size do not start another
// slow wait, while a different pane size may retry.
func TestTimeoutKeepsTheFailedSizeButAllowsADifferentOne(t *testing.T) {
	surface := &Surface{
		frameNumber:     1,
		frame:           image.Rect(0, 0, 200, 150),
		resizeInFlight:  true,
		resizeRequested: image.Pt(160, 120),
	}
	surface.applyResizeResult(appsession.ErrDesktopResizeTimeout)
	if surface.resizeInFlight {
		t.Fatal("a timeout left the resize in flight")
	}
	if surface.resizeRequested != image.Pt(160, 120) {
		t.Fatalf("timed-out size = %v, want the failed size kept as backoff", surface.resizeRequested)
	}
	if surface.resizeUnsupported {
		t.Fatal("a timeout latched the fallback instead of allowing a retry")
	}

	surface.mu.Lock()
	same := surface.shouldRequestDesktopSize(160, 120)
	different := surface.shouldRequestDesktopSize(80, 60)
	surface.mu.Unlock()
	if same {
		t.Fatal("the timed-out size was retried on a redraw")
	}
	if !different {
		t.Fatal("a different pane size was not allowed to retry")
	}
}

// TestStaleNonzeroReplyDoesNotLatchTheFallback proves the viewer's half of the
// stale-reply rule: a late nonzero answer for an older request is surfaced by
// the adapter as a timeout, and a timeout must never latch local scaling.
func TestStaleNonzeroReplyDoesNotLatchTheFallback(t *testing.T) {
	surface := &Surface{
		frameNumber:     1,
		frame:           image.Rect(0, 0, 64, 48),
		resizeInFlight:  true,
		resizeRequested: image.Pt(64, 48),
	}
	surface.applyResizeResult(appsession.ErrDesktopResizeTimeout)
	if surface.resizeUnsupported {
		t.Fatal("a stale nonzero reply latched the local scaling fallback")
	}
	if surface.resizeRequested != image.Pt(64, 48) {
		t.Fatalf("timed-out size = %v, want the per-size backoff kept", surface.resizeRequested)
	}
}

// TestStageFourWithoutFollowUpSuppressesTheSameSizeButRetries proves a
// forwarded (status 4) request with no follow-up keeps the requested size: a
// redraw at that size is suppressed, a different pane size retries, and the
// fallback never latches.
func TestStageFourWithoutFollowUpSuppressesTheSameSizeButRetries(t *testing.T) {
	surface := &Surface{
		frameNumber:     1,
		frame:           image.Rect(0, 0, 200, 150),
		resizeInFlight:  true,
		resizeRequested: image.Pt(160, 120),
	}
	surface.applyResizeResult(appsession.ErrDesktopResizePending)
	if surface.resizeInFlight {
		t.Fatal("a pending reply left the resize in flight")
	}
	if surface.resizeUnsupported {
		t.Fatal("a forwarded (status 4) reply latched the local scaling fallback")
	}
	surface.mu.Lock()
	same := surface.shouldRequestDesktopSize(160, 120)
	different := surface.shouldRequestDesktopSize(80, 60)
	surface.mu.Unlock()
	if same {
		t.Fatal("the pending size was re-requested on a redraw")
	}
	if !different {
		t.Fatal("a different pane size was not allowed to retry")
	}
}
