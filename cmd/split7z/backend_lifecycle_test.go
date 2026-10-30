package main

import (
	"testing"
	"time"
)

func TestBackendSurvivesSlowStartupAndSuspension(t *testing.T) {
	start := time.Unix(1700000000, 0)
	// No request during Edge's slow startup: must not time out after 28s.
	if shouldStopBackend(start.Add(90*time.Second), start, start, time.Time{}, time.Time{}, false, false) {
		t.Fatal("slow startup incorrectly stopped the service")
	}
	// The window is minimized for hours; the browser process still exists.
	if shouldStopBackend(start.Add(4*time.Hour), start, start.Add(5*time.Second), time.Time{}, time.Time{}, true, false) {
		t.Fatal("background polling throttle incorrectly stopped the service")
	}
}

func TestBackendGracefulCloseAndReload(t *testing.T) {
	start := time.Unix(1700000000, 0)
	closed := start.Add(time.Minute)
	if shouldStopBackend(closed.Add(10*time.Second), start, closed.Add(-time.Second), closed, time.Time{}, true, false) {
		t.Fatal("closed page stopped without a reload grace period")
	}
	if !shouldStopBackend(closed.Add(25*time.Second), start, closed.Add(-time.Second), closed, time.Time{}, true, false) {
		t.Fatal("page closed, service did not stop")
	}
	if shouldStopBackend(closed.Add(25*time.Second), start, closed.Add(3*time.Second), closed, time.Time{}, true, false) {
		t.Fatal("reload reconnect should cancel closing")
	}
	if shouldStopBackend(closed.Add(25*time.Second), start, closed.Add(-time.Second), closed, time.Time{}, true, true) {
		t.Fatal("compression must outlive the UI")
	}
}

func TestBackendProcessExitGrace(t *testing.T) {
	start := time.Unix(1700000000, 0)
	// A quickly exited launcher may have handed the live window to another process.
	exit := start.Add(3 * time.Second)
	if shouldStopBackend(start.Add(2*time.Minute), start, start.Add(time.Minute), time.Time{}, exit, true, false) {
		t.Fatal("launcher hand-off incorrectly stopped service")
	}
	// A normal browser process exiting should allow eventual cleanup.
	exit = start.Add(5 * time.Minute)
	if !shouldStopBackend(exit.Add(4*time.Minute), start, exit.Add(-5*time.Second), time.Time{}, exit, true, false) {
		t.Fatal("exited browser should eventually stop service")
	}
}
