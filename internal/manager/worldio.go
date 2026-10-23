package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/worlds"
)

type ctxT = context.Context

var timeNow = time.Now

// PrepareDownload zips a world for downloading. It returns the zip's path
// (the caller deletes it) and a name for the file, like "Midgard
// 2026-10-23.zip".
func (m *Manager) PrepareDownload(name string) (path, filename string, err error) {
	lib := m.library()
	if err := os.MkdirAll(lib.Staging, 0o755); err != nil {
		return "", "", err
	}
	path, err = lib.ExportFile(name, lib.Staging)
	if err != nil {
		return "", "", fmt.Errorf("Couldn't pack %q: %v", name, err)
	}
	return path, fmt.Sprintf("%s %s.zip", name, timeNow().Format("2006-01-02")), nil
}

// ExportBackup streams a backup as a zip file.
func (m *Manager) ExportBackup(w io.Writer, name, id string) error {
	return m.library().ExportBackup(w, name, id)
}

// NewUpload starts an upload of world files.
func (m *Manager) NewUpload() (*worlds.Upload, error) {
	m.library().CleanUploads(2 * time.Hour)
	u, err := m.library().NewUpload()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.uploads[u.ID] = u
	m.mu.Unlock()
	return u, nil
}

// FinishUpload scans an upload for worlds.
func (m *Manager) FinishUpload(id string) ([]worlds.Candidate, error) {
	u, err := m.upload(id)
	if err != nil {
		return nil, err
	}
	cands := u.Scan(m.library())
	if len(cands) == 0 {
		m.DiscardUpload(id)
		return nil, errors.New("No Valheim worlds were found in that. Add the world's folder (Valheim 1.0), its .fwl and .db files (older versions), or a zip of either.")
	}
	m.markInUse(cands)
	return cands, nil
}

// markInUse flags candidates that would replace the world the server is
// playing.
func (m *Manager) markInUse(cands []worlds.Candidate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range cands {
		cands[i].InUse = cands[i].Exists && m.worldBusyLocked(cands[i].Name)
	}
}

func (m *Manager) upload(id string) (*worlds.Upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[id]
	if !ok {
		return nil, errors.New("That upload has expired. Please upload the world again.")
	}
	return u, nil
}

// DiscardUpload deletes an upload that won't be imported.
func (m *Manager) DiscardUpload(id string) {
	m.mu.Lock()
	u, ok := m.uploads[id]
	delete(m.uploads, id)
	m.mu.Unlock()
	if ok {
		u.Remove()
	}
}

// ImportUpload puts the chosen worlds from an upload on the server and
// returns the job doing it.
func (m *Manager) ImportUpload(id string, choices []worlds.Choice) (int, error) {
	u, err := m.upload(id)
	if err != nil {
		return 0, err
	}
	return m.startImport(u.Candidates, choices, true, func() { m.DiscardUpload(id) })
}

// LocalWorlds finds worlds saved by the Valheim game on this computer.
func (m *Manager) LocalWorlds() []worlds.Candidate {
	home, _ := os.UserHomeDir()
	sources := worlds.GameSaveSources(home, platform.SteamPath(), isWindows)
	cands := m.library().LocalCandidates(sources)
	m.markInUse(cands)
	m.mu.Lock()
	m.localCands = cands
	m.mu.Unlock()
	if cands == nil {
		return []worlds.Candidate{}
	}
	return cands
}

// ImportLocal copies chosen worlds from this computer's game saves and
// returns the job doing it.
func (m *Manager) ImportLocal(choices []worlds.Choice) (int, error) {
	m.mu.Lock()
	cands := m.localCands
	m.mu.Unlock()
	if len(cands) == 0 {
		return 0, errors.New("Look for worlds on this computer first.")
	}
	return m.startImport(cands, choices, false, nil)
}

// startImport puts chosen worlds into the worlds folder as a job (moving
// them, or copying with move=false). Replacing the world the server is
// playing stops the server first, which saves it, so the current state is
// in the backup made before replacing; the server then starts again with
// the new copy. after runs when the job ends.
func (m *Manager) startImport(cands []worlds.Candidate, choices []worlds.Choice, move bool, after func()) (int, error) {
	names := chosenNames(cands, choices)
	if len(names) == 0 {
		if after != nil {
			after()
		}
		return 0, errors.New("Nothing was chosen to add.")
	}
	if m.replacesRunning(names) {
		m.endStartWait() // don't wait for a start that is only loading the world
	}
	title := "Adding worlds"
	if len(names) == 1 {
		for name, replace := range names {
			title = fmt.Sprintf("Adding %q", name)
			if replace {
				title = fmt.Sprintf("Replacing %q", name)
			}
		}
	}
	return m.startJob("import", title, false, func(ctx ctxT, j *jobCtl) error {
		if after != nil {
			defer after()
		}
		restart := m.replacesRunning(names)
		if restart {
			if err := m.doStop(ctx, j); err != nil {
				return err
			}
		}
		before, _, _ := m.library().List()
		j.step("Putting the world in place…", -1)
		busy := func(name string) bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.worldBusyLocked(name)
		}
		done, err := m.library().Import(cands, choices, busy, move)
		for _, p := range done {
			if p.Replaced {
				m.addActivity("import", "", fmt.Sprintf("The world %q was replaced with a new copy. The old one is kept as a backup.", p.Name))
			} else {
				m.addActivity("import", "", fmt.Sprintf("The world %q was added.", p.Name))
			}
		}
		if restart {
			j.step("Starting the server again…", -1)
			if serr := m.doStart(ctx, j); serr != nil {
				if err == nil {
					return fmt.Errorf("%s But the server didn't start again: %w", importResult(done), serr)
				}
				m.log.Printf("restart after a failed import: %v", serr)
			}
		}
		if err != nil {
			return err
		}
		msg := importResult(done)
		if restart {
			msg += " The server restarted with it."
		}
		// On a new server, the first world added is the one to play.
		if len(before) == 0 && len(done) == 1 && !m.serverRunning() {
			if err := m.SelectWorld(done[0].Name, false); err == nil {
				msg += " It's now the world the server plays."
			}
		}
		j.result(msg)
		return nil
	})
}

func (m *Manager) serverRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.run != nil
}

// chosenNames maps the world names the choices put on the server to
// whether each replaces a world.
func chosenNames(cands []worlds.Candidate, choices []worlds.Choice) map[string]bool {
	byID := map[string]worlds.Candidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	names := map[string]bool{}
	for _, ch := range choices {
		c, ok := byID[ch.ID]
		if !ok || ch.Action == "skip" {
			continue
		}
		name := strings.TrimSpace(ch.Name)
		if name == "" {
			name = c.Name
		}
		names[name] = ch.Action == "replace"
	}
	return names
}

func (m *Manager) replacesRunning(names map[string]bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, replace := range names {
		if replace && m.worldBusyLocked(name) {
			return true
		}
	}
	return false
}

func importResult(done []worlds.Placed) string {
	switch {
	case len(done) == 0:
		return "Nothing was added."
	case len(done) == 1 && done[0].Replaced:
		return fmt.Sprintf("%q was replaced. The server's old copy is kept as a backup.", done[0].Name)
	case len(done) == 1:
		return fmt.Sprintf("%q was added to the server.", done[0].Name)
	}
	var added, replaced []string
	for _, p := range done {
		if p.Replaced {
			replaced = append(replaced, fmt.Sprintf("%q", p.Name))
		} else {
			added = append(added, fmt.Sprintf("%q", p.Name))
		}
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "Added "+strings.Join(added, ", ")+".")
	}
	if len(replaced) > 0 {
		parts = append(parts, "Replaced "+strings.Join(replaced, ", ")+" (the old copies are kept as backups).")
	}
	return strings.Join(parts, " ")
}

// Upload returns an upload in progress.
func (m *Manager) Upload(id string) (*worlds.Upload, error) { return m.upload(id) }
