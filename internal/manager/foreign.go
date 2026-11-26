package manager

// Foreign servers: other games' servers started by a newer version of the
// app (spec 3.6 and 4.4). After the automatic rollback from a newer build,
// they keep running, and this version can't control them: it only knows
// they are there from their run records (games/<id>/run.json). While their
// process lives it keeps the computer awake, never lets it sleep by itself
// (their players can't be seen, so someone may be playing), and says so
// on the panel. It never touches their files.

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// foreignRun is a live server this version only watches.
type foreignRun struct {
	id   string // its folder under games/
	name string // the game's name, from the run record
	pid  int
}

// openProc is platform.OpenProc; the quickness tests count its calls.
var openProc = platform.OpenProc

// scanForeign reads every games/*/run.json and keeps the servers whose
// process is still the one the record names: the same PID, started within
// 2 seconds of the record's start time (a PID can be used again by another
// program). It runs when the app starts and with the checks every minute,
// never in Status, so the panel stays quick.
func (m *Manager) scanForeign() {
	files, _ := filepath.Glob(filepath.Join(m.opt.DataDir, "games", "*", "run.json"))
	var live []foreignRun
	for _, path := range files {
		id := filepath.Base(filepath.Dir(path))
		if _, ours := m.instances[id]; ours {
			continue // an instance this version runs: adoption takes care of it
		}
		rec, err := loadRuntime(path)
		if err != nil || !sameProcess(rec) {
			continue
		}
		name := rec.Name
		if name == "" {
			name = rec.Game
		}
		if name == "" {
			name = id
		}
		live = append(live, foreignRun{id: id, name: name, pid: rec.PID})
	}
	sort.Slice(live, func(i, j int) bool { return live[i].id < live[j].id })
	m.mu.Lock()
	changed := fmt.Sprint(live) != fmt.Sprint(m.foreign)
	m.foreign = live
	m.mu.Unlock()
	if changed {
		for _, f := range live {
			m.log.Printf("%s (pid %d) is running, started by a newer version of the app", f.name, f.pid)
		}
		m.updateAwake()
		m.notify()
	}
}

// sameProcess reports whether the process a run record names is still
// running: the same PID, started when the record says.
func sameProcess(rec game.Record) bool {
	if rec.Started.IsZero() {
		return false
	}
	proc, err := openProc(rec.PID)
	if err != nil {
		return false
	}
	defer proc.Close()
	return absDuration(proc.StartTime().Sub(rec.Started)) < 2*time.Second
}

// foreignWarningsLocked are the panel's warnings about foreign servers.
func (m *Manager) foreignWarningsLocked() []string {
	var out []string
	for _, f := range m.foreign {
		out = append(out, fmt.Sprintf("%s is running, started by a newer version of the app; this version can't control it.", f.name))
	}
	return out
}
