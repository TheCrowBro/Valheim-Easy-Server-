package valheim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/mods"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/steamcmd"
)

// ActionError is an error with the button the panel offers next to it
// ("worlds": open the Worlds page). The manager shows Action the way it
// shows its own errors' suggested next step.
type ActionError struct {
	Msg    string
	Action string
}

func (e *ActionError) Error() string { return e.Msg }

// SuggestedAction is the panel's next step for this error.
func (e *ActionError) SuggestedAction() string { return e.Action }

// Plan gets a start ready, exactly as the manager did before the move:
// it updates the server when that setting is on, installs and switches on
// the world's mods, checks the port, and returns the command line.
func (s *Server) Plan(ctx context.Context, req game.PlanRequest) (game.Plan, error) {
	step := req.Step
	if step == nil {
		step = func(string, float64) {}
	}
	st, p := s.settingsFor(req.Saved, req.World), s.store.Paths()
	if req.Update {
		step("Checking for Valheim server updates…", -1)
		err := s.store.Installer().Install(ctx, false, func(pr steamcmd.Progress) {
			msg := pr.Step
			if msg == "Server files are up to date." {
				msg = "The server is up to date."
			}
			step(msg, pr.Percent)
		})
		if ctx.Err() != nil {
			return game.Plan{}, ctx.Err()
		}
		if err != nil {
			s.store.Note("warning", "Couldn't check for server updates ("+err.Error()+"). Starting the version that's installed.")
		}
	}
	if err := s.prepareMods(ctx, step, st, p); err != nil {
		return game.Plan{}, err
	}
	if req.Cancellable != nil {
		req.Cancellable(false)
	}
	if platform.UDPPortInUse(st.Port) {
		return game.Plan{}, fmt.Errorf("Port %d is already being used by another program, maybe another Valheim server. Close it or pick another port in Settings.", st.Port)
	}
	for _, dir := range []string{p.Save, p.Worlds, p.Logs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return game.Plan{}, err
		}
	}
	// The log is named when the server is launched, after the update and
	// the mods, as it always was.
	logPath := filepath.Join(p.Logs, "server-"+time.Now().Format("2006-01-02_15-04-05")+".log")
	args, err := st.ServerArgs(p.Save, logPath)
	if err != nil {
		return game.Plan{}, err
	}
	env := []string{"SteamAppId=892970"}
	if runtime.GOOS == "darwin" {
		// The macOS server comes without Steam's client library: SteamCMD
		// has one, for both kinds of Mac.
		env = append(env, "DYLD_FALLBACK_LIBRARY_PATH="+p.SteamCMD+":/usr/local/lib:/usr/lib")
	}
	env = append(env, mods.Env(p.Server, len(mods.ServerMods(st.WorldMods())) > 0)...)
	rec := game.Record{Server: st.ServerName, Port: st.Port, Password: st.Password}
	setNoCrossplay(&rec, !st.Crossplay)
	setCodeMods(&rec, sortedIDs(st.WorldMods()))
	return game.Plan{
		Exe: p.ServerExe, Args: args, Dir: p.Server, Env: env,
		Output: game.LogArg, LogPath: logPath,
		ReadyWait:   ReadyWait,
		Fingerprint: ArgsKey(st, p.Save),
		Record:      rec,
	}, nil
}

// prepareMods installs, updates and switches on the mods the selected world
// is played with, or switches the mod loader off when it has none.
func (s *Server) prepareMods(ctx context.Context, step func(string, float64), st config.Settings, p config.Paths) error {
	in := &mods.Installer{Dir: p.Server, URL: s.store.ModsURL()}
	ids := st.WorldMods()
	if len(mods.ServerMods(ids)) > 0 {
		step("Getting the mods ready…", -1)
	}
	res, err := in.Prepare(ctx, ids, st.UpdateOnStart, step)
	s.setInstalledMods(p.Server, in.Installed())
	for _, c := range res.Changed {
		s.store.Note("update", c)
	}
	if res.Warning != "" {
		s.store.Note("warning", res.Warning)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return &ActionError{Msg: fmt.Sprintf("Couldn't get the mods ready (%v). To start without them, turn them off on the world's card on the Worlds page.", err), Action: "worlds"}
	}
	return nil
}

func sortedIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return slices.Sorted(slices.Values(ids))
}

// Stop asks Valheim to save and quit with Ctrl+C (SIGINT elsewhere), once
// more after 90 seconds, and gives up after 4 minutes, as always.
func (s *Server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{{Kind: "interrupt", Wait: 4 * time.Minute, Resend: 90 * time.Second}}
}

// ExitHint is the most likely reason the server stopped, from the last
// lines of its log (oldest first).
func (s *Server) ExitHint(tail []string) string { return ExitHint(tail) }

// ExitHint is Server.ExitHint without a server.
func ExitHint(tail []string) string {
	for i := len(tail) - 1; i >= 0; i-- {
		t := tail[i]
		switch {
		case strings.Contains(t, "Error bad password"):
			return "Valheim rejected the password: it can't be part of the server name, the world name or the seed."
		case strings.Contains(t, "World load failed"):
			return "The world file couldn't be loaded. It may be damaged: restore a backup on the Worlds page."
		case strings.Contains(strings.ToLower(t), "address already in use"), strings.Contains(t, "Failed to bind"):
			return "The network port is in use by another program."
		}
	}
	return "Open the Log page to see what happened."
}
