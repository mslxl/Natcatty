package wasm

import (
	"testing"
	"time"
)

func TestWatchdogFiresAfterTheGrace(t *testing.T) {
	fired := make(chan struct{})
	stop := startWatchdog(10*time.Millisecond, func() { close(fired) })
	defer stop()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the watchdog did not fire after the grace period")
	}
}

func TestWatchdogStopsWhenTheCallReturns(t *testing.T) {
	fired := make(chan struct{}, 1)
	stop := startWatchdog(50*time.Millisecond, func() { fired <- struct{}{} })
	stop()
	select {
	case <-fired:
		t.Fatal("the watchdog fired after a normal return")
	case <-time.After(120 * time.Millisecond):
	}
}
