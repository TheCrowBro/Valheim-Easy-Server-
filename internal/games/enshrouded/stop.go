package enshrouded

import (
	"time"

	"valheim-easy-server/internal/game"
)

// Stop asks the server to save the world and quit: Ctrl+C, built from
// nothing but the run record, so it works the same after a takeover (spec
// 2.7).
//
// enshrouded_server.exe is a console program (its PE header says so,
// FACTS.md), so the app's Ctrl+C reaches it through its own hidden
// console. It answers with "[app] Trigger gameflow shutdown, exit: Ctrl_C",
// shuts its world down, which saves it, and exits: within a few seconds on
// the servers hosting projects run. The server has no console commands, no
// RCON and no web API, so Ctrl+C is its only gentle stop.
//
// The wait is long, in case a big world on a slow disk takes its time to
// save; Ctrl+C goes again every 90 seconds in case one was missed. The app
// never kills the server by itself: only the owner's Force stop does.
func (s *server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{{Kind: "interrupt", Wait: 4 * time.Minute, Resend: 90 * time.Second}}
}
