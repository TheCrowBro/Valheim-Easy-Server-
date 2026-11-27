package manager

// Removing a game (spec 6.2, stream A): a job in that game's own slot
// ("remove") that stops it, backs each world up into its backups, removes
// its router mappings, deletes its server, runtime and logs, and drops it
// from servers.json and the stars. Its backups, players and records stay
// in games/<id>/ and show again if it is added back; its firewall rules go
// at the next fix. Removing the legacy game only hides it (I1): stopped,
// unstarred, no command, never started by anything, files untouched.
//
// The worlds themselves stay too (games/<id>/saves): the spec lists what
// is deleted, and a world is the one thing that can't be downloaded again,
// so it is never deleted by removing a game, backup or not.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/servers"
)

// RemoveGame removes (or, for the legacy game, hides) a game and answers
// the job's id (POST /api/games/{id}/remove).
func (m *Manager) RemoveGame(id string) (int, error) {
	inst, err := m.instanceByID(id)
	if err != nil {
		return 0, err
	}
	if inst.legacy {
		return m.hideLegacy(inst)
	}
	if inst.srv == nil {
		return 0, fmt.Errorf("This version of the app can't remove %s: a newer version added it.", inst.short())
	}
	m.endStartWait(inst)
	return m.startInstJob(inst, "remove", "Removing "+inst.short(), false, func(ctx context.Context, j *jobCtl) error {
		return m.doRemove(ctx, j, inst)
	})
}

// doRemove is the remove job of a non-legacy game. Each step must work
// before the next one deletes anything: a world that couldn't be backed up,
// or a list of games that couldn't be written, keeps the server files too,
// so nothing is lost and the owner can try again. Files that can't be
// deleted at the end (a program holding them) are only left over: the game
// is gone from the panel all the same.
func (m *Manager) doRemove(ctx context.Context, j *jobCtl, inst *instance) error {
	short := inst.short()
	if err := m.doStop(ctx, j, inst); err != nil {
		return err
	}
	m.mu.Lock()
	still := inst.run != nil
	m.mu.Unlock()
	if still {
		return fmt.Errorf("The %s server is still running, so it wasn't removed.", short)
	}

	list, _, err := inst.srv.Worlds().List()
	if err != nil {
		return fmt.Errorf("Couldn't list %s's worlds to back them up, so nothing was removed: %v", short, err)
	}
	for _, w := range list {
		j.step(fmt.Sprintf("Backing up the world %q…", w.Name), -1)
		if _, err := inst.srv.Worlds().Backup(w.Name, "removed"); err != nil {
			return fmt.Errorf("Couldn't back up the world %q, so nothing was removed: %v", w.Name, err)
		}
	}
	if r, ok := inst.srv.(game.Remover); ok {
		j.step("Tidying up after "+short+"…", -1)
		if err := r.Remove(ctx); err != nil {
			m.log.Printf("removing %s: the game's own tidy-up: %v", inst.id, err)
		}
	}
	m.unmapPorts(inst) // router ports, if any are left from its last run

	err = m.updateServers(func(f *servers.File) error {
		f.Instances = slices.DeleteFunc(f.Instances, func(in servers.Instance) bool { return in.ID == inst.id })
		f.App.Stars = slices.DeleteFunc(f.App.Stars, func(s string) bool { return s == inst.id })
		f.App.DisabledGames = slices.DeleteFunc(f.App.DisabledGames, func(s string) bool { return s == inst.id })
		return nil
	})
	if err != nil {
		return fmt.Errorf("Couldn't take %s off the list of games, so nothing was removed: %v", short, err)
	}
	m.mu.Lock()
	m.removeInstanceLocked(inst.id)
	m.resumeSet = slices.DeleteFunc(m.resumeSet, func(s string) bool { return s == inst.id })
	m.mu.Unlock()
	m.starsChanged() // its Discord command goes

	j.step("Deleting the "+short+" server files…", -1)
	dirs := m.dirsOf(inst)
	var left []string
	for _, dir := range []string{dirs.Server, dirs.Runtime, dirs.Logs, dirs.Staging} {
		if err := m.removeInside(inst, dir); err != nil {
			m.log.Printf("removing %s: %v", inst.id, err)
			left = append(left, dir)
		}
	}
	os.Remove(m.runFile(inst))
	done := fmt.Sprintf("%s was removed. Its worlds and their backups are kept in %s: add %s again to play them.",
		short, filepath.Join("games", inst.id), short)
	if len(left) > 0 {
		done += " Some of its server files couldn't be deleted (a program may be using them): " + strings.Join(left, ", ") + "."
	}
	m.addGameActivity(inst, "info", "", done)
	j.result(done)
	m.notify()
	return nil
}

// removeInside deletes one of the instance's folders, but only one inside
// games/<id>: whatever a game's folders say, removing it never deletes
// anything else (the legacy game's folders, the owner's own game's saves).
func (m *Manager) removeInside(inst *instance, dir string) error {
	if dir == "" {
		return nil
	}
	root := filepath.Join(m.opt.DataDir, "games", inst.id)
	rel, err := filepath.Rel(root, dir)
	if !servers.ValidID(inst.id) || err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		m.log.Printf("removing %s: left %s alone (it isn't inside %s)", inst.id, dir, root)
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("Couldn't delete %s (%v). Close any program using it and try again.", dir, err)
	}
	return nil
}

// hideLegacy "removes" the legacy game: hidden, so it is stopped, unstarred,
// left out of the sidebar, start-with-app, the resume set and the waker's
// commands, and never started by anything until it is added again; its
// files stay exactly where they are (invariant I1). It is a job in its own
// slot, as removing any game is. servers.json is written first, so nothing
// can start it while it stops.
func (m *Manager) hideLegacy(inst *instance) (int, error) {
	m.mu.Lock()
	hidden := m.isHiddenLocked(inst)
	m.mu.Unlock()
	if hidden {
		return 0, fmt.Errorf("%s is removed from the panel already.", inst.short())
	}
	m.endStartWait(inst)
	return m.startInstJob(inst, "remove", "Removing "+inst.short()+" from the panel", false, func(ctx context.Context, j *jobCtl) error {
		err := m.updateServers(func(f *servers.File) error {
			e := f.Instance(inst.id)
			if e == nil {
				f.Instances = append(f.Instances, servers.Instance{ID: inst.id, Game: inst.info.ID, Legacy: true})
				e = &f.Instances[len(f.Instances)-1]
			}
			e.Hidden = true
			f.App.Stars = slices.DeleteFunc(f.App.Stars, func(s string) bool { return s == inst.id })
			return nil
		})
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.resumeSet = slices.DeleteFunc(m.resumeSet, func(s string) bool { return s == inst.id })
		m.mu.Unlock()
		m.starsChanged() // its Discord command goes
		if err := m.doStop(ctx, j, inst); err != nil {
			return err
		}
		done := fmt.Sprintf("%s was removed from the panel. Its worlds, settings and files are all kept: add %s again to play it.", inst.short(), inst.short())
		m.addGameActivity(inst, "info", "", done)
		j.result(done)
		m.notify()
		return nil
	})
}
