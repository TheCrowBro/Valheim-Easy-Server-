package palworld

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
)

// Recover makes a record for a server found running without its run file.
// The REST API's port and this run's admin password are what the probe and
// the stop need, and both are in the PalWorldSettings.ini the plan wrote
// for this run (the server reads it only when it starts), so they are read
// back from there. Without them the stop could only be Ctrl+C.
//
// The rest is what the manager would use without a Recoverer: the selected
// world, this instance's newest log and the program.
func (s *server) Recover(pid int, started time.Time) game.Record {
	saved := s.store.Get()
	rec := game.Record{
		PID: pid, Started: started, Launched: started,
		World: saved.World, Exe: s.Program(),
		LogPath: newestLog(s.env.Dirs.Logs, s.env.Instance),
		Game:    ID, Instance: s.env.Instance, Name: Game{}.Info().Short,
		Ports:   map[string]int{},
		Secrets: map[string]string{},
	}
	gamePort, restPort := ports(saved)
	rec.Port, rec.Ports["game"], rec.Ports["rest"] = gamePort, gamePort, restPort
	rec.Server, rec.Password = s.value(saved, "serverName"), saved.Server["password"]
	if b, err := os.ReadFile(s.settingsPath()); err == nil {
		if opts, ok := parseINI(string(b)).optionsLine(); ok {
			if v, ok := opts.get("RESTAPIPort"); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
					rec.Ports["rest"] = n
				}
			}
			if v, ok := opts.get("AdminPassword"); ok {
				rec.Secrets[restSecret] = unquote(v)
			}
		}
	}
	return rec
}

// newestLog is the instance's newest log file ("" when it has none).
func newestLog(dir, instance string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, instance+"-*.log"))
	var best string
	var bestTime time.Time
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.ModTime().After(bestTime) {
			best, bestTime = m, st.ModTime()
		}
	}
	return best
}
