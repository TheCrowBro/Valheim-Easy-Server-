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

// ExportWorld streams a world as a zip file.
func (m *Manager) ExportWorld(w io.Writer, name string) error {
	return m.lib.Export(w, name)
}

// ExportBackup streams a backup as a zip file.
func (m *Manager) ExportBackup(w io.Writer, name, id string) error {
	return m.lib.ExportBackup(w, name, id)
}

// NewUpload starts an upload of world files.
func (m *Manager) NewUpload() (*worlds.Upload, error) {
	m.lib.CleanUploads(2 * time.Hour)
	u, err := m.lib.NewUpload()
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
	cands := u.Scan(m.lib)
	if len(cands) == 0 {
		m.DiscardUpload(id)
		return nil, errors.New("No Valheim worlds were found in what you uploaded. Upload the world's folder (Valheim 1.0), its .fwl and .db files (older versions), or a zip of either.")
	}
	return cands, nil
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

// ImportUpload imports the chosen worlds from an upload.
func (m *Manager) ImportUpload(id string, choices []worlds.Choice) error {
	u, err := m.upload(id)
	if err != nil {
		return err
	}
	return m.runJob("import", "Importing worlds", false, func(_ ctxT, j *jobCtl) error {
		defer m.DiscardUpload(id)
		j.step("Moving the worlds into place…", -1)
		return m.finishImport(j, u.Candidates, choices, true)
	})
}

// LocalWorlds finds worlds saved by the Valheim game on this computer.
func (m *Manager) LocalWorlds() []worlds.Candidate {
	home, _ := os.UserHomeDir()
	sources := worlds.GameSaveSources(home, platform.SteamPath(), isWindows)
	cands := m.lib.LocalCandidates(sources)
	m.mu.Lock()
	m.localCands = cands
	m.mu.Unlock()
	if cands == nil {
		return []worlds.Candidate{}
	}
	return cands
}

// ImportLocal copies chosen worlds from this computer's game saves.
func (m *Manager) ImportLocal(choices []worlds.Choice) error {
	m.mu.Lock()
	cands := m.localCands
	m.mu.Unlock()
	if len(cands) == 0 {
		return errors.New("Look for worlds on this computer first.")
	}
	return m.runJob("import", "Importing worlds", false, func(_ ctxT, j *jobCtl) error {
		j.step("Copying the worlds…", -1)
		return m.finishImport(j, cands, choices, false)
	})
}

func (m *Manager) finishImport(j *jobCtl, cands []worlds.Candidate, choices []worlds.Choice, move bool) error {
	busy := func(name string) bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.worldBusyLocked(name)
	}
	done, err := m.lib.Import(cands, choices, busy, move)
	for _, name := range done {
		m.addActivity("import", "", fmt.Sprintf("The world %q was imported.", name))
	}
	if err != nil {
		return err
	}
	if len(done) == 0 {
		j.result("Nothing was imported.")
	} else {
		j.result("Imported " + strings.Join(quoteAll(done), ", ") + ".")
	}
	return nil
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

// Upload returns an upload in progress.
func (m *Manager) Upload(id string) (*worlds.Upload, error) { return m.upload(id) }
