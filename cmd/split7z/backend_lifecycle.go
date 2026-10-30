package main

import "time"

// shouldStopBackend does not use periodic browser polling as a death signal.
// A suspended/minimized page can skip polls for arbitrarily long periods.
// Instead wait for an actual close hint, or for a browser process that has
// exited and has not reconnected for a conservative period of time.
func shouldStopBackend(now, startedAt, lastSeen, pageClosedAt, edgeExitedAt time.Time, clientSeen, running bool) bool {
	if running {
		return false // a running compression job is never interrupted by closing its UI
	}
	if !pageClosedAt.IsZero() && !lastSeen.After(pageClosedAt) && now.Sub(pageClosedAt) > 20*time.Second {
		return true
	}
	if edgeExitedAt.IsZero() {
		return false
	}
	// A browser process can exit quickly after handing off the new window to
	// another Edge process. Avoid treating such a launcher exit as UI closure.
	if edgeExitedAt.Sub(startedAt) < 15*time.Second {
		if !clientSeen {
			return now.Sub(startedAt) > 5*time.Minute
		}
		return now.Sub(lastSeen) > 8*time.Hour
	}
	if !clientSeen {
		return now.Sub(startedAt) > 5*time.Minute
	}
	// Allow for slow browser restart, machine suspend/resume and delayed polls.
	return now.Sub(edgeExitedAt) > 3*time.Minute && now.Sub(lastSeen) > 3*time.Minute
}
