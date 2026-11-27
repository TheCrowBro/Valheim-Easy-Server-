package manager

// The "still playing?" rule (spec 4.6, stream D), for a game whose
// presence is "none" (Windrose until a real log proves its lines, any game
// whose signal turns out unusable) or not yet proven on this install (2.6).
// 4 hours after it is ready the app asks in its channel whether it should
// stay on; any message there that isn't from a bot or a webhook, or Keep it
// on in the panel, restarts the clock; with no answer in 2 hours it posts
// "Turning … off" and stops it (stopAsked, through autoStopGame). With no
// channel at all the question shows only in the panel (10.2 Q2).
//
// Phase 0' made this file with its signatures and empty bodies; stream D
// fills them in. askObserver is D's observer (hooks.go).

import (
	"time"

	"valheim-easy-server/internal/game"
)

// askState is one instance's "still playing?" clock (instance.ask), guarded
// by m.mu. D adds its fields here.
type askState struct{}

// AskStatus is a game's "ask" in Status (4.9): the clock and the question
// on its Home and the Overview, with Keep it on.
type AskStatus struct {
	Since   time.Time  `json:"since"`             // when the 4-hour clock last started
	AskAt   time.Time  `json:"askAt"`             // when the question goes out
	Asked   *time.Time `json:"asked,omitempty"`   // when it went out, once it has
	StopAt  *time.Time `json:"stopAt,omitempty"`  // when the game turns off without an answer
	CanRead bool       `json:"canRead"`           // the bot can read the channel, so any message there counts
	Channel string     `json:"channel,omitempty"` // the channel's name, for "The bot can't read #channel…"
	Problem string     `json:"problem,omitempty"` // why it can't ("give the bot View Channel and Read Message History there")
}

// askStatusLocked is the instance's "still playing?" state for Status, or
// nil when the rule doesn't apply to it now.
func (m *Manager) askStatusLocked(inst *instance) *AskStatus {
	return nil
}

// KeepOn restarts the "still playing?" clock of a running game, as a
// message in its channel would (POST /api/games/{id}/keep-on).
func (m *Manager) KeepOn(id string) error {
	if _, err := m.instanceByID(id); err != nil {
		return err
	}
	return errNotYet
}

// askObserver is stream D's observer (hooks.go): the "still playing?"
// clock, and anything else Discord needs to hear about. Every method runs
// with m.mu held and must not block.
type askObserver struct{ m *Manager }

func (o askObserver) onReady(inst *instance)                                {}
func (o askObserver) onEvent(inst *instance, ev game.Event)                 {}
func (o askObserver) onExit(inst *instance, run *serverRun, exit *ExitInfo) {}
func (o askObserver) onTick(now time.Time)                                  {}
func (o askObserver) onJob(inst *instance, job *Job)                        {}
func (o askObserver) onTakeover(inst *instance)                             {}
func (o askObserver) onWake(cause string)                                   {}
