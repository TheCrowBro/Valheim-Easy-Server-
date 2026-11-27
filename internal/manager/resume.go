package manager

// Waking up and the resume (spec 4.7, stream A). Going to sleep records the
// games that were running (resumeSet); waking on purpose waits until the
// network is really back, then 30 s more, and starts them again, unless a
// Discord request was heard since the wake, which starts only its own game
// (startFromWake). The KVM's own Wake-on-LAN button (an untagged packet)
// resumes at once.
//
// Phase 0' made this file with its signatures and empty bodies; stream A
// fills them in. Stream WK's wakesignal.go calls startFromWake and
// resumeNow.

import (
	"context"
)

// startFromWake starts the game a Discord request's wake packet asks for
// (id, the instance; tag, the request): only that game, once per request,
// cancelling any pending resume and noting wakeAsked. It runs without m.mu.
// An error says why it can't start (memory short, busy, turned off, not
// installed); WK's wakesignal.go then posts that in the game's channel
// (wakeReply, waker.go).
func (m *Manager) startFromWake(id, tag string) error {
	return errNotYet
}

// scheduleResume starts the resume set once the network is back and 30 s
// more have passed, unless a Discord request came first. wokenOnPurpose
// (power.go) calls it.
func (m *Manager) scheduleResume() {}

// resumeNow starts the resume set at once, if it hasn't been yet (an
// untagged wake packet; WK's wakesignal.go calls it).
func (m *Manager) resumeNow() {}

// cancelResumeLocked stops a resume that hasn't started its games yet; the
// ones already starting go on.
func (m *Manager) cancelResumeLocked() {}

// networkBack waits until the network is really back after a wake: the
// wake listener is bound again and the default gateway answers a TCP
// connection on port 53 or 80 (accepted or refused, within 1 s), checked
// every 2 s for at most 3 minutes. It reports whether it came back.
func (m *Manager) networkBack(ctx context.Context) bool {
	return true
}
