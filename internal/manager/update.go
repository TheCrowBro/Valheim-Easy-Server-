package manager

// The app updating itself from GitHub: it looks for a newer build a couple
// of minutes after starting and then every hour, installs it when nothing
// is in progress, and restarts. The Valheim server keeps running, and the
// restarted app takes it over again. If a new build doesn't start, the
// previous version is put back (see main), and that build isn't installed
// automatically again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/selfupdate"
)

// AppInfo is this app's version and what the last update check found.
type AppInfo struct {
	Version   string              `json:"version"`
	Build     int                 `json:"build"` // 0 for a copy that doesn't update itself
	Latest    *selfupdate.Release `json:"latest,omitempty"`
	CheckedAt *time.Time          `json:"checkedAt,omitempty"`
	Error     string              `json:"error,omitempty"`
	Updating  bool                `json:"updating,omitempty"`
	Failed    int                 `json:"failed,omitempty"` // a build that didn't start on this computer
}

// appUpdate is what the last check found.
type appUpdate struct {
	latest    *selfupdate.Release // newer than this app
	checkedAt time.Time
	err       string
	updating  bool
	failed    int // a build that didn't start
	warned    int // the build a failed update was reported for
}

// updateRecord is kept in update.json while the app restarts into a new
// build, so the restarted app can tell whether that worked. A build that
// didn't start is remembered.
type updateRecord struct {
	From   int    `json:"from,omitempty"`
	To     int    `json:"to,omitempty"`
	Title  string `json:"title,omitempty"`
	Failed int    `json:"failed,omitempty"`
}

const updateFile = "update.json"

var (
	errUpdateBusy      = errors.New("Please wait until the job or upload in progress has finished, then update the app.")
	errUpdatingAlready = errors.New("The app is updating already.")
	errUpdatingBusy    = &BusyError{Title: "updating the app"}
)

func (m *Manager) canUpdate() bool {
	return m.opt.Updater != nil && m.opt.AppBuild > 0 && m.opt.Relaunch != nil && m.opt.AppExe != ""
}

func (m *Manager) appInfoLocked() AppInfo {
	a := AppInfo{Version: m.opt.AppVersion, Latest: m.app.latest, Error: m.app.err, Updating: m.app.updating, Failed: m.app.failed}
	if m.canUpdate() {
		a.Build = m.opt.AppBuild
	}
	if !m.app.checkedAt.IsZero() {
		at := m.app.checkedAt
		a.CheckedAt = &at
	}
	return a
}

// busyLocked reports whether a job is running or someone is uploading a
// world (an upload left alone for hours doesn't count).
func (m *Manager) busyLocked() bool {
	if m.job != nil && m.job.Finished == nil {
		return true
	}
	for _, u := range m.uploads {
		if time.Since(u.Created) < 2*time.Hour {
			return true
		}
	}
	return false
}

// noteUpdate reports how the update that restarted the app went.
func (m *Manager) noteUpdate() {
	var rec updateRecord
	b, err := os.ReadFile(m.dataFile(updateFile))
	if err != nil || json.Unmarshal(b, &rec) != nil {
		return
	}
	switch {
	case rec.To == 0:
	case m.opt.AppBuild == rec.To:
		text := fmt.Sprintf("The app was updated to build %d", rec.To)
		if rec.Title != "" {
			text += ": " + rec.Title
		}
		m.addActivity("update", "", text+".")
	case m.opt.AppBuild == rec.From:
		m.addActivity("warning", "", fmt.Sprintf("Build %d of the app didn't start on this computer, so the app went back to build %d. It won't install build %d by itself again.", rec.To, rec.From, rec.To))
		rec.Failed = rec.To
	}
	if rec.Failed <= m.opt.AppBuild {
		rec.Failed = 0 // this build is that one or newer
	}
	rec.From, rec.To, rec.Title = 0, 0, ""
	m.mu.Lock()
	m.app.failed = rec.Failed
	m.mu.Unlock()
	m.saveUpdateRecord(rec)
}

func (m *Manager) saveUpdateRecord(rec updateRecord) {
	path := m.dataFile(updateFile)
	if rec == (updateRecord{}) {
		os.Remove(path)
		return
	}
	if b, err := json.Marshal(rec); err == nil {
		config.WriteFileAtomic(path, b, 0o644)
	}
}

// updateLoop checks for a new build now and then, and installs it when
// automatic updates are on.
func (m *Manager) updateLoop() {
	if !m.canUpdate() {
		return
	}
	wait := 2 * time.Minute
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		m.mu.Lock()
		auto := m.settings.AutoUpdate
		m.mu.Unlock()
		if err := m.checkForUpdate(auto, true); errors.Is(err, errUpdateBusy) {
			wait = 10 * time.Minute
		} else if err != nil {
			m.log.Printf("app update: %v", err)
		}
	}
}

// CheckForUpdate looks for a newer build of the app on GitHub and, with
// install, installs it and restarts the app.
func (m *Manager) CheckForUpdate(install bool) error { return m.checkForUpdate(install, false) }

// checkForUpdate is CheckForUpdate. Automatic updates skip a build that
// didn't start before.
func (m *Manager) checkForUpdate(install, auto bool) error {
	if !m.canUpdate() {
		return errors.New("This copy of the app doesn't update itself.")
	}
	m.mu.Lock()
	updating := m.app.updating
	m.mu.Unlock()
	if updating {
		return errUpdatingAlready
	}
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
	defer cancel()
	checkCtx, cancelCheck := context.WithTimeout(ctx, 30*time.Second)
	rel, found, err := m.opt.Updater.Latest(checkCtx)
	cancelCheck()

	m.mu.Lock()
	m.app.checkedAt = time.Now()
	m.app.err, m.app.latest = "", nil
	if err != nil {
		m.app.err = err.Error()
	} else if found && rel.Build > m.opt.AppBuild {
		m.app.latest = &rel
	}
	install = install && m.app.latest != nil && !(auto && rel.Build == m.app.failed)
	if install {
		switch {
		case m.app.updating:
			err = errUpdatingAlready
		case m.busyLocked():
			err = errUpdateBusy
		default:
			m.app.updating = true // no new jobs or uploads from here on
		}
	}
	failed := m.app.failed
	m.mu.Unlock()
	m.notify()
	if err != nil || !install {
		return err
	}

	exe := m.opt.AppExe
	next := exe + ".new"
	err = m.opt.Updater.Download(ctx, rel, next)
	if err == nil {
		err = selfupdate.Swap(exe, next)
	}
	if err != nil {
		os.Remove(next)
		m.mu.Lock()
		m.app.updating, m.app.err = false, err.Error()
		warn := m.app.warned != rel.Build
		m.app.warned = rel.Build
		m.mu.Unlock()
		if warn {
			m.addActivity("warning", "", fmt.Sprintf("Couldn't update the app to build %d (%v).", rel.Build, err))
		}
		m.notify()
		return err
	}
	m.saveUpdateRecord(updateRecord{From: m.opt.AppBuild, To: rel.Build, Title: rel.Title, Failed: failed})
	text := fmt.Sprintf("Updating the app to build %d", rel.Build)
	if rel.Title != "" {
		text += ": " + rel.Title
	}
	m.addActivity("update", "", text+". The Valheim server keeps running while the app restarts.")
	m.saveData()
	m.notify()
	go func() {
		time.Sleep(time.Second) // let the panel hear about it first
		m.opt.Relaunch()
	}()
	return nil
}
