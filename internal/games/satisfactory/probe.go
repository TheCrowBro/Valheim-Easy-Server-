package satisfactory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"valheim-easy-server/internal/game"
)

// Probe asks the API how many are on (QueryServerState's
// numConnectedPlayers, which a player can't fake). The server counts as
// up, so the Monitor marks it online, once it plays the run's world and
// the app's setup isn't still busy with it: before that, friends couldn't
// join the right world with the right password yet. An answer while it
// loads or sets up is up=false without an error; only a server that
// doesn't answer is a failed probe.
//
// The probe also starts the setup when nothing has for this run yet
// (setup.go).
func (s *server) Probe(ctx context.Context, rec game.Record) (game.ProbeResult, error) {
	r := s.ensureSetup(rec)
	var st serverGameState
	err := s.asAdmin(ctx, rec, func(c *apiClient) (e error) {
		st, e = c.queryState(ctx)
		return e
	})
	if err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	target := s.worlds.sessionOf(rec.World)
	playing := st.IsGameRunning && (target == "" || sameSession(st.ActiveSessionName, target))
	return game.ProbeResult{Up: playing && !r.running(), Count: st.NumConnectedPlayers}, nil
}

// Stop asks the API to save and then shut down, the wiki's way; Ctrl+C is
// the fallback when the API doesn't answer. Ctrl+C is sent once: Unreal
// Engine programs may take a second one as "quit now", which could cut a
// save short.
func (s *server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{
		{Kind: "call", Call: func(ctx context.Context) error { return s.apiStop(ctx, rec) }, Wait: stopWait},
		{Kind: "interrupt", Wait: interruptWait,
			Say: "Satisfactory's server API didn't answer, so the server is being stopped with Ctrl+C. It may not save on the way out: anything built since its last autosave (every 5 minutes) can be lost."},
	}
}

// apiStop saves the world being played under its own name, then shuts the
// server down.
//
// A save that fails is noted and the shutdown still goes: it is the
// server's clean way out, which Ctrl+C may not be. A shutdown whose answer
// is cut off (the server closing as it answers) counts as sent; one that
// can't be sent at all, or that the server turns down, gives its error, and
// the chain goes on to Ctrl+C at once.
func (s *server) apiStop(ctx context.Context, rec game.Record) error {
	return s.asAdmin(ctx, rec, func(c *apiClient) error {
		if st, err := c.queryState(ctx); err == nil && st.IsGameRunning && st.ActiveSessionName != "" {
			if err := c.saveGame(ctx, st.ActiveSessionName); err != nil {
				if authFailed(err) {
					return err // logged in again, then tried once more
				}
				s.log.Printf("saving before the shutdown: %v", err)
			}
		} else if authFailed(err) {
			return err
		}
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err := c.shutdown(sctx)
		var ae *apiError
		var pe *pinError
		if err == nil || errors.As(err, &ae) || errors.As(err, &pe) || refused(err) {
			return err
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || isReset(err) {
			return nil
		}
		return err
	})
}

// isReset says the server closed the connection under the request.
func isReset(err error) bool {
	var ne *net.OpError
	return errors.As(err, &ne) && !ne.Timeout() && ne.Op == "read"
}

// Recover makes a record for a server found running without its run file:
// the ports and the admin password the app keeps for it, so the probe and
// the stop work as for a run the app started. The rest is what the manager
// would use without a Recoverer.
func (s *server) Recover(pid int, started time.Time) game.Record {
	saved := s.store.Get()
	gamePort, reliablePort := ports(saved)
	rec := game.Record{
		PID: pid, Started: started, Launched: started,
		World: saved.World, Exe: s.Program(), LogPath: s.logPath(),
		Played:   game.Played{Mods: map[string]string{}},
		Server:   serverName(saved),
		Port:     gamePort,
		Password: saved.Server["password"],
		Game:     ID, Instance: s.env.Instance, Name: Game{}.Info().Short,
		Ports:   map[string]int{"game": gamePort, "reliable": reliablePort},
		Secrets: map[string]string{},
	}
	if a := saved.State[stateAdmin]; a != "" {
		rec.Secrets[secretAdmin] = a
	}
	return rec
}

// Remove tidies up when the owner removes the game. The worlds stay where
// the server keeps them (spec 2.8), but the server's own settings for the
// port, which hold the claim with the admin password the app made, are set
// aside: the app forgets that password with the game, and a server still
// claimed with it could never be set up again by the app if the game is
// added back. The file is renamed, not deleted, so nothing is lost.
func (s *server) Remove(ctx context.Context) error {
	st := s.store.Get().State
	if st[stateClaimed] == "" || st[stateClaimPort] == "" {
		return nil
	}
	port, err := strconv.Atoi(st[stateClaimPort])
	if err != nil {
		return nil
	}
	path := s.settingsFile(port)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	aside := path + ".removed-" + time.Now().UTC().Format("2006-01-02_15-04-05")
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("couldn't set Satisfactory's server settings aside (%s): %w", filepath.Base(path), err)
	}
	return nil
}
