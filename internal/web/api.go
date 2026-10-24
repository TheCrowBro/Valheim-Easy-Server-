package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/stats"
	"valheim-easy-server/internal/worlds"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	a := mux.HandleFunc

	a("GET /{$}", s.index)
	mux.Handle("GET /assets/", s.static())

	a("GET /api/status", s.status)
	a("POST /api/server/start", s.act(func(r *http.Request) error { return s.m.StartServer() }))
	a("POST /api/server/stop", s.act(func(r *http.Request) error { return s.m.StopServer() }))
	a("POST /api/server/restart", s.act(func(r *http.Request) error { return s.m.RestartServer() }))
	a("POST /api/server/force-stop", s.act(func(r *http.Request) error { return s.m.ForceStop() }))
	a("POST /api/server/install", s.install)
	a("POST /api/job/cancel", s.act(func(r *http.Request) error { return s.m.CancelJob() }))
	a("POST /api/power/hibernate", s.act(func(r *http.Request) error { return s.m.Hibernate("") }))
	a("POST /api/power/keep-awake", s.act(func(r *http.Request) error { s.m.KeepAwake(); return nil }))
	a("POST /api/power/enable-hibernate", s.act(func(r *http.Request) error { return s.m.EnableHibernation() }))
	a("POST /api/power/enable-wake-on-lan", s.act(func(r *http.Request) error { return s.m.EnableWakeOnLAN() }))

	a("GET /api/worlds", s.worlds)
	a("POST /api/worlds/select", s.selectWorld)
	a("POST /api/worlds/create", s.createWorld)
	a("POST /api/worlds/options", s.worldOptions)
	a("POST /api/worlds/mods", s.worldMods)
	a("POST /api/worlds/rename", s.renameWorld)
	a("POST /api/worlds/delete", s.worldAction(func(name, _ string) error { return s.m.DeleteWorld(name) }))
	a("POST /api/worlds/backup", s.worldAction(func(name, _ string) error { return s.m.BackupWorld(name) }))
	a("POST /api/worlds/restore", s.worldAction(func(name, id string) error { return s.m.RestoreWorld(name, id) }))
	a("GET /api/worlds/download", s.downloadWorld)
	a("GET /api/worlds/backup-download", s.downloadBackup)
	a("POST /api/uploads", s.newUpload)
	a("POST /api/uploads/{id}/files", s.uploadFiles)
	a("POST /api/uploads/{id}/scan", s.scanUpload)
	a("POST /api/uploads/{id}/import", s.importUpload)
	a("DELETE /api/uploads/{id}", s.discardUpload)
	a("GET /api/local-worlds", s.localWorlds)
	a("POST /api/local-worlds/import", s.importLocal)

	a("GET /api/settings", s.getSettings)
	a("POST /api/settings", s.saveSettings)
	a("POST /api/discord/test", s.testDiscord)
	a("POST /api/app/update", s.updateApp)
	a("GET /api/lists", s.getLists)
	a("POST /api/lists", s.saveList)
	a("GET /api/players", s.players)

	a("GET /api/stats", s.stats)
	a("POST /api/stats/reset", s.act(func(r *http.Request) error { s.m.ResetStats(); return nil }))
	a("GET /api/logs", s.logs)
	a("GET /api/logs/download", s.downloadLog)

	a("POST /api/system/firewall", s.act(func(r *http.Request) error { return s.m.FixFirewall() }))
	a("POST /api/system/open-folder", s.openFolder)
	a("POST /api/system/launch-on-login", s.launchOnLogin)
	a("POST /api/system/quit", s.quitApp)
	return securityHeaders(guard(mux))
}

// --- helpers ------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// fail turns an error into a response the panel can show.
func fail(w http.ResponseWriter, err error) {
	var fe config.FieldErrors
	var ae *manager.ActionError
	var be *manager.BusyError
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Please check the highlighted settings.", "fields": fe})
	case errors.As(err, &ae):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": ae.Msg, "action": ae.Action})
	case errors.As(err, &be):
		writeErr(w, http.StatusConflict, be.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("the request couldn't be read: %v", err)
	}
	return nil
}

func ok(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) }

// act wraps a simple action with no input.
func (s *Server) act(fn func(r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(r); err != nil {
			fail(w, err)
			return
		}
		ok(w)
	}
}

// --- status & server ---------------------------------------------------

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.m.Status())
}

func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repair bool `json:"repair"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.InstallServer(body.Repair); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// --- worlds -----------------------------------------------------------------

func (s *Server) worlds(w http.ResponseWriter, r *http.Request) {
	v, err := s.m.Worlds()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) selectWorld(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Restart bool   `json:"restart"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.SelectWorld(body.Name, body.Restart); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) createWorld(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string              `json:"name"`
		Options config.WorldOptions `json:"options"`
		Restart bool                `json:"restart"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.CreateWorld(body.Name, body.Options, body.Restart); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) worldOptions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string              `json:"name"`
		Options config.WorldOptions `json:"options"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if msg := config.WorldNameProblem(body.Name); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.m.SetWorldOptions(body.Name, body.Options); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) worldMods(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string   `json:"name"`
		Mods []string `json:"mods"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if msg := config.WorldNameProblem(body.Name); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.m.SetWorldMods(body.Name, body.Mods); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) renameWorld(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		NewName string `json:"newName"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.RenameWorld(body.Name, body.NewName); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) worldAction(fn func(name, id string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name     string `json:"name"`
			BackupID string `json:"backupId"`
		}
		if err := readJSON(r, &body); err != nil {
			fail(w, err)
			return
		}
		if msg := config.WorldNameProblem(body.Name); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		if err := fn(body.Name, body.BackupID); err != nil {
			fail(w, err)
			return
		}
		ok(w)
	}
}

func attachment(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "no-store")
}

// downloadWorld packs a world into a zip first, so problems are reported
// properly and the browser knows the size (for its progress bar).
func (s *Server) downloadWorld(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if msg := config.WorldNameProblem(name); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	path, filename, err := s.m.PrepareDownload(name)
	if err != nil {
		fail(w, err)
		return
	}
	defer os.Remove(path)
	f, err := os.Open(path)
	if err != nil {
		fail(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fail(w, err)
		return
	}
	attachment(w, filename)
	http.ServeContent(w, r, filename, st.ModTime(), f)
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	name, id := r.URL.Query().Get("name"), r.URL.Query().Get("id")
	if msg := config.WorldNameProblem(name); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	attachment(w, name+" backup.zip")
	if err := s.m.ExportBackup(w, name, id); err != nil {
		s.log.Printf("download backup %s/%s: %v", name, id, err)
	}
}

func (s *Server) newUpload(w http.ResponseWriter, r *http.Request) {
	u, err := s.m.NewUpload()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": u.ID})
}

// uploadFiles receives files as multipart form data, streaming each one to
// disk so even large worlds don't need much memory. The file name may
// include folders (when a whole world folder was chosen).
func (s *Server) uploadFiles(w http.ResponseWriter, r *http.Request) {
	u, err := s.m.Upload(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, worlds.MaxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "The upload couldn't be read.")
		return
	}
	count := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "The upload was interrupted. Please try again.")
			return
		}
		name := rawFileName(part.Header.Get("Content-Disposition"))
		if name == "" {
			part.Close()
			continue
		}
		if err := u.AddFile(name, part); err != nil {
			part.Close()
			fail(w, err)
			return
		}
		part.Close()
		count++
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": count})
}

// rawFileName reads the file name from a part header, keeping any folder
// part (Go's Part.FileName would drop it).
func rawFileName(cd string) string {
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	if v := params["filename"]; v != "" {
		if dec, err := url.PathUnescape(v); err == nil && strings.Contains(v, "%") {
			return dec
		}
		return v
	}
	return ""
}

func (s *Server) scanUpload(w http.ResponseWriter, r *http.Request) {
	cands, err := s.m.FinishUpload(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": cands})
}

func (s *Server) importUpload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Choices []worlds.Choice `json:"choices"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	job, err := s.m.ImportUpload(r.PathValue("id"), body.Choices)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": job})
}

func (s *Server) discardUpload(w http.ResponseWriter, r *http.Request) {
	s.m.DiscardUpload(r.PathValue("id"))
	ok(w)
}

func (s *Server) localWorlds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"candidates": s.m.LocalWorlds()})
}

func (s *Server) importLocal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Choices []worlds.Choice `json:"choices"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	job, err := s.m.ImportLocal(body.Choices)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": job})
}

// --- settings ---------------------------------------------------------------

type settingsView struct {
	Settings      config.Settings     `json:"settings"`
	Presets       []string            `json:"presets"`
	Modifiers     map[string][]string `json:"modifiers"`
	ModifierOrder []string            `json:"modifierOrder"`
	Keys          []string            `json:"keys"`
	Paths         config.Paths        `json:"paths"`
	LaunchOnLogin bool                `json:"launchOnLogin"`
	Version       string              `json:"version"` // this app's
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v := settingsView{
		Settings:      s.m.Settings(),
		Presets:       config.Presets,
		Modifiers:     config.Modifiers,
		ModifierOrder: config.ModifierOrder,
		Keys:          config.Keys,
		Paths:         s.m.Paths(),
		Version:       s.version,
	}
	v.LaunchOnLogin, _ = platform.LaunchOnLogin()
	writeJSON(w, http.StatusOK, v)
}

// updateApp checks GitHub for a newer build of the app, and installs it
// when asked to.
func (s *Server) updateApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Install bool `json:"install"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.CheckForUpdate(body.Install); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// testDiscord posts a test message with the webhook link from the form.
func (s *Server) testDiscord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Webhook string `json:"webhook"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.TestDiscord(body.Webhook); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var in config.Settings
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.UpdateSettings(in); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) getLists(w http.ResponseWriter, r *http.Request) {
	lists, err := s.m.Lists()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lists)
}

func (s *Server) saveList(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind config.ListKind `json:"kind"`
		IDs  []string        `json:"ids"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.SetList(body.Kind, body.IDs); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) players(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"players": s.m.KnownPlayers()})
}

// --- stats & logs -------------------------------------------------------

var ranges = map[string]time.Duration{
	"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour,
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	d, okRange := ranges[r.URL.Query().Get("range")]
	if !okRange {
		d = time.Hour
	}
	samples := stats.Downsample(s.m.History(time.Now().Add(-d)), 360)
	if samples == nil {
		samples = []stats.Sample{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"samples":   samples,
		"byPlayers": s.m.ByPlayers(),
		"machine":   s.m.Machine(),
	})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	lines, last := s.m.Logs(after, 1000)
	if lines == nil {
		lines = []manager.LogLine{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "last": last})
}

func (s *Server) downloadLog(w http.ResponseWriter, r *http.Request) {
	path := s.m.CurrentLogPath()
	f, err := os.Open(path)
	if path == "" || err != nil {
		writeErr(w, http.StatusNotFound, "There's no server log yet.")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	io.Copy(w, f)
}

// --- this computer ---------------------------------------------------------

func (s *Server) openFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Which string `json:"which"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	p := s.m.Paths()
	dirs := map[string]string{"worlds": p.Worlds, "backups": p.Backups, "logs": p.Logs, "server": p.Server, "data": p.Data, "saves": p.Save}
	dir, okDir := dirs[body.Which]
	if !okDir {
		writeErr(w, http.StatusBadRequest, "unknown folder")
		return
	}
	if err := platform.OpenFolder(dir); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) launchOnLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.SetLaunchOnLogin(body.Enabled); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) quitApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StopServer bool `json:"stopServer"`
	}
	if err := readJSON(r, &body); err != nil {
		fail(w, err)
		return
	}
	ok(w)
	if s.quit != nil {
		go s.quit(body.StopServer)
	}
}
