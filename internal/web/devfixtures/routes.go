package main

// The control panel's routes (spec 6.2), answered from the made-up games.
// The patterns are the app's own (internal/web's newRoutes and the old
// routes the new panel still reads), and devfixtures_test.go checks that
// every one of the app's routes has an answer here, so the two lists can't
// drift apart unnoticed.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/worlds"
)

func newHandler(st *state, ui string) http.Handler {
	mux := http.NewServeMux()
	h := &handler{st: st, ui: ui}
	mux.HandleFunc("GET /{$}", h.index)
	mux.Handle("GET /assets/", noCache(http.FileServer(http.Dir(ui))))
	for _, rt := range h.routes() {
		mux.HandleFunc(rt.pattern, rt.fn)
	}
	// Anything else under /api/: a change is taken as done; a read says
	// there's no made-up answer for it yet (the test keeps that from
	// happening to any of the app's routes).
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeErr(w, http.StatusNotImplemented, "devfixtures has no made-up answer for GET "+r.URL.Path+" yet.")
			return
		}
		ok(w)
	})
	return guard(mux)
}

type handler struct {
	st *state
	ui string
}

type route struct {
	pattern string
	fn      http.HandlerFunc
}

func (h *handler) routes() []route {
	g := h.game
	return []route{
		// The new panel's (spec 6.2).
		{"GET /api/status", h.locked(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, h.st.statusLocked()) })},
		{"GET /api/catalog", h.locked(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, h.st.catalogLocked()) })},
		{"GET /api/catalog/{game}/local", h.locked(h.catalogLocal)},
		{"POST /api/games", h.locked(h.addGame)},
		{"POST /api/games/{id}/remove", g(h.remove)},
		{"POST /api/games/{id}/star", g(h.star)},
		{"POST /api/app/sidebar", h.locked(h.sidebar)},
		{"POST /api/games/{id}/start", g(h.start)},
		{"POST /api/games/{id}/stop", g(h.stop)},
		{"POST /api/games/{id}/restart", g(h.restart)},
		{"POST /api/games/{id}/force-stop", g(h.forceStop)},
		{"POST /api/games/{id}/cancel", g(h.cancel)},
		{"POST /api/games/{id}/install", g(h.install)},
		{"POST /api/games/{id}/keep-on", g(func(w http.ResponseWriter, r *http.Request, inst *instance) { inst.ask = nil; ok(w) })},
		{"GET /api/games/{id}/settings", g(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			writeJSON(w, http.StatusOK, h.st.settingsLocked(inst))
		})},
		{"POST /api/games/{id}/settings", g(h.saveSettings)},
		{"POST /api/games/{id}/ports/pick", g(h.pickPorts)},
		{"GET /api/games/{id}/worlds", g(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			writeJSON(w, http.StatusOK, h.st.worldsLocked(inst))
		})},
		{"POST /api/games/{id}/worlds/select", g(h.selectWorld)},
		{"POST /api/games/{id}/worlds/create", g(h.createWorld)},
		{"POST /api/games/{id}/worlds/delete", g(h.deleteWorld)},
		{"POST /api/games/{id}/worlds/backup", g(h.backupWorld)},
		{"POST /api/games/{id}/worlds/restore", g(h.restoreWorld)},
		{"POST /api/games/{id}/worlds/rename", g(h.renameWorld)},
		{"POST /api/games/{id}/worlds/values", g(h.worldValues)},
		{"GET /api/games/{id}/worlds/download", g(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			sendZip(w, r.URL.Query().Get("name")+" "+h.st.now().Format("2006-01-02")+".zip")
		})},
		{"GET /api/games/{id}/worlds/backup-download", g(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			sendZip(w, r.URL.Query().Get("name")+" backup.zip")
		})},
		{"POST /api/games/{id}/uploads", g(h.newUpload)},
		{"POST /api/games/{id}/uploads/{upload}/files", h.uploadFiles}, // reads the body without the lock
		{"POST /api/games/{id}/uploads/{upload}/scan", g(h.scanUpload)},
		{"POST /api/games/{id}/uploads/{upload}/import", g(h.importWorlds)},
		{"DELETE /api/games/{id}/uploads/{upload}", g(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			delete(h.st.uploads, r.PathValue("upload"))
			ok(w)
		})},
		{"GET /api/games/{id}/local-worlds", g(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			writeJSON(w, http.StatusOK, map[string]any{"candidates": h.st.localCandidates(inst.info)})
		})},
		{"POST /api/games/{id}/local-worlds/import", g(h.importWorlds)},
		{"GET /api/games/{id}/players", g(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			writeJSON(w, http.StatusOK, map[string]any{"players": append([]manager.KnownPlayer{}, inst.known...)})
		})},
		{"GET /api/games/{id}/records", g(h.records)},
		{"GET /api/games/{id}/lists", g(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			writeJSON(w, http.StatusOK, h.st.listsLocked(inst))
		})},
		{"POST /api/games/{id}/lists", g(h.setList)},
		{"POST /api/games/{id}/allow", g(h.allow)},
		{"GET /api/games/{id}/stats", g(h.stats)},
		{"POST /api/games/{id}/stats/reset", g(func(w http.ResponseWriter, r *http.Request, inst *instance) { ok(w) })},
		{"GET /api/games/{id}/logs", g(h.logs)},
		{"GET /api/games/{id}/logs/download", g(h.downloadLog)},
		{"GET /api/router", h.locked(h.router)},
		{"POST /api/router/check", h.locked(func(w http.ResponseWriter, r *http.Request) { ok(w) })},
		{"POST /api/app/settings", h.locked(h.appSettings)},
		{"POST /api/discord/main", h.locked(h.mainDiscord)},
		{"POST /api/discord/test", h.locked(h.testDiscord)},
		{"POST /api/power/stop-all", h.locked(h.stopAll)},

		// The old routes the new panel may still read, answered for the
		// legacy game (as the app does).
		{"GET /api/settings", h.locked(h.oldSettings)},
		{"GET /api/jetkvm", h.locked(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, manager.WakerView{})
		})},
		{"GET /api/worlds", h.legacy(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			writeJSON(w, http.StatusOK, h.st.worldsLocked(inst))
		})},
		{"GET /api/players", h.legacy(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			writeJSON(w, http.StatusOK, map[string]any{"players": append([]manager.KnownPlayer{}, inst.known...)})
		})},
		{"GET /api/records", h.legacy(h.records)},
		{"GET /api/lists", h.legacy(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			out := map[string][]string{}
			for _, l := range h.st.listsLocked(inst) {
				out[l.Kind.ID] = l.IDs
			}
			writeJSON(w, http.StatusOK, out)
		})},
		{"GET /api/stats", h.legacy(h.stats)},
		{"GET /api/logs", h.legacy(h.logs)},
		{"GET /api/logs/download", h.legacy(h.downloadLog)},
		{"GET /api/local-worlds", h.legacy(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			writeJSON(w, http.StatusOK, map[string]any{"candidates": h.st.localCandidates(inst.info)})
		})},
		{"GET /api/worlds/download", h.legacy(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			sendZip(w, r.URL.Query().Get("name")+".zip")
		})},
		{"GET /api/worlds/backup-download", h.legacy(func(w http.ResponseWriter, r *http.Request, inst *instance) {
			sendZip(w, r.URL.Query().Get("name")+" backup.zip")
		})},
	}
}

// --- the panel's files and the guard ------------------------------------------------

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(filepath.Join(h.ui, "index.html"))
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// guard refuses what the app's guard refuses (internal/web/auth.go): a
// request from another computer, and a change without the panel's X-VES
// header. So a panel page that forgets the header fails here too, not only
// in the app.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			writeErr(w, http.StatusForbidden, "The control panel only works on the server computer itself.")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet && r.Header.Get("X-VES") != "1" {
			writeErr(w, http.StatusForbidden, "This request was blocked for safety. Reload the page and try again.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- answers --------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func ok(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]any{"ok": true}) }

// fail answers as the app's web.fail does: {error, fields?, action?,
// data?}, 409 while another job is in the way.
func fail(w http.ResponseWriter, err error) {
	var fe game.FieldErrors
	var cfe config.FieldErrors
	var ae *manager.ActionError
	var b busy
	switch {
	case errors.As(err, &fe):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fe.Error(), "fields": fe})
	case errors.As(err, &cfe):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": cfe.Error(), "fields": cfe})
	case errors.As(err, &ae):
		body := map[string]any{"error": ae.Msg, "action": ae.Action}
		if ae.Data != nil {
			body["data"] = ae.Data
		}
		writeJSON(w, http.StatusBadRequest, body)
	case errors.As(err, &b):
		writeErr(w, http.StatusConflict, b.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

// read reads a JSON body as the app does: empty is {}, and numbers and
// true/false inside "values" objects are taken as text. (This is a copy of
// internal/web's readBody, which isn't exported; keep the two alike.)
func read(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("the request couldn't be read: %v", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("the request couldn't be read: %v", err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if vals, isMap := c.(map[string]any); isMap && k == "values" {
					for key, val := range vals {
						switch t := val.(type) {
						case json.Number:
							vals[key] = t.String()
						case bool:
							vals[key] = strconv.FormatBool(t)
						}
					}
				}
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(doc)
	b, _ = json.Marshal(doc)
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("the request couldn't be read: %v", err)
	}
	return nil
}

// locked runs fn with the state's lock held.
func (h *handler) locked(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.st.mu.Lock()
		defer h.st.mu.Unlock()
		fn(w, r)
	}
}

// game runs fn for the game {id}, with the lock held.
func (h *handler) game(fn func(http.ResponseWriter, *http.Request, *instance)) http.HandlerFunc {
	return h.locked(func(w http.ResponseWriter, r *http.Request) {
		inst := h.st.find(r.PathValue("id"))
		if inst == nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("There's no game %q on this computer.", r.PathValue("id")))
			return
		}
		fn(w, r, inst)
	})
}

// legacy runs fn for the legacy game, for the old routes.
func (h *handler) legacy(fn func(http.ResponseWriter, *http.Request, *instance)) http.HandlerFunc {
	return h.locked(func(w http.ResponseWriter, r *http.Request) {
		for _, inst := range h.st.insts {
			if inst.info.Legacy {
				fn(w, r, inst)
				return
			}
		}
		writeErr(w, http.StatusBadRequest, "The main game has been removed from the panel.")
	})
}

func sendZip(w http.ResponseWriter, name string) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("made up by devfixtures.txt")
	f.Write([]byte("A made-up download: the control panel's dev fixtures have no real worlds.\n"))
	zw.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Write(buf.Bytes())
}

// --- starting and stopping -------------------------------------------------------------

func (h *handler) start(w http.ResponseWriter, r *http.Request, inst *instance) {
	var o manager.StartOptions
	if err := read(r, &o); err != nil {
		fail(w, err)
		return
	}
	if err := h.st.startLocked(inst, o.Memory); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (st *state) startLocked(inst *instance, memory string) error {
	info := inst.info
	switch l, why := info.LevelOn(st.opt.GOOS, st.opt.GOARCH); {
	case !inst.installed:
		return &manager.ActionError{Msg: fmt.Sprintf("The %s server isn't installed yet. Click Install first.", info.Short), Action: "install"}
	case inst.disabled:
		return fmt.Errorf("%s is turned off. Turn it on again on its Settings tab first.", info.Short)
	case l == game.CantRun:
		return fmt.Errorf("%s can't run on this computer: %s.", info.Short, strings.TrimSuffix(why, "."))
	case inst.state != manager.StateStopped:
		return fmt.Errorf("%s is already running.", info.Short)
	}
	if err := st.busyLocked(inst); err != nil {
		return err
	}
	switch {
	case memory == "anyway":
	case strings.HasPrefix(memory, "stop:"):
		other := st.find(strings.TrimPrefix(memory, "stop:"))
		if other == nil || other.state != manager.StateOnline {
			return errors.New("That game isn't running any more.")
		}
		st.goOffline(other, true)
	case memory == "":
		if free := st.freeMBLocked(); info.MemoryMB > free {
			return &manager.ActionError{
				Msg: fmt.Sprintf("There may not be enough memory to start %s: it needs about %s and %s is free.",
					info.Short, gb(info.MemoryMB), gb(free)),
				Action: "memory",
				Data:   manager.MemoryShort{NeedMB: info.MemoryMB, FreeMB: free, Running: st.runningGamesLocked()}}
		}
	default:
		return fmt.Errorf("the request couldn't be read: memory %q", memory)
	}
	inst.state = manager.StateStarting
	st.job(inst, "start", "Starting "+info.Short, "Starting the server…", true, func() {
		st.goOnline(inst, 0, st.now())
		gen := inst.gen
		// Somebody joins a moment later.
		time.AfterFunc(st.opt.Step, func() {
			st.mu.Lock()
			defer st.mu.Unlock()
			if inst.gen == gen && inst.state == manager.StateOnline {
				p := game.Player{Name: names[3], Account: "76561198000000004", Since: st.now()}
				inst.players = append(inst.players, p)
				st.logLine(inst, p.Name+" joined the game")
				st.note(inst, "join", p.Name+" joined "+inst.info.Short+".")
			}
		})
	})
	return nil
}

func (h *handler) stop(w http.ResponseWriter, r *http.Request, inst *instance) {
	if inst.state != manager.StateOnline {
		writeErr(w, http.StatusBadRequest, inst.info.Short+" isn't running.")
		return
	}
	if err := h.st.busyLocked(inst); err != nil {
		fail(w, err)
		return
	}
	inst.state = manager.StateStopping
	h.st.job(inst, "stop", "Stopping "+inst.info.Short, "Saving the world and shutting down…", false, func() { h.st.goOffline(inst, true) })
	ok(w)
}

func (h *handler) restart(w http.ResponseWriter, r *http.Request, inst *instance) {
	if inst.state != manager.StateOnline {
		writeErr(w, http.StatusBadRequest, inst.info.Short+" isn't running.")
		return
	}
	if err := h.st.busyLocked(inst); err != nil {
		fail(w, err)
		return
	}
	inst.state = manager.StateStopping
	h.st.job(inst, "restart", "Restarting "+inst.info.Short, "Saving the world and shutting down…", false, func() {
		h.st.goOffline(inst, true)
		h.st.goOnline(inst, 0, h.st.now())
	})
	ok(w)
}

func (h *handler) forceStop(w http.ResponseWriter, r *http.Request, inst *instance) {
	if inst.state == manager.StateStopped {
		writeErr(w, http.StatusBadRequest, inst.info.Short+" isn't running.")
		return
	}
	if inst.job != nil && inst.job.Finished == nil {
		t := h.st.now()
		inst.job.Finished, inst.job.Error = &t, "Stopped at once."
	}
	h.st.goOffline(inst, false)
	ok(w)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request, inst *instance) {
	j := inst.job
	if j == nil || j.Finished != nil {
		ok(w)
		return
	}
	if !j.Cancellable {
		writeErr(w, http.StatusBadRequest, "This can't be cancelled at this point.")
		return
	}
	t := h.st.now()
	j.Finished, j.Error = &t, "Cancelled."
	if inst.state == manager.StateStarting {
		inst.state = manager.StateStopped
	}
	ok(w)
}

func (h *handler) install(w http.ResponseWriter, r *http.Request, inst *instance) {
	var body struct {
		Repair bool `json:"repair"`
	}
	if err := read(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := h.st.busyLocked(inst); err != nil {
		fail(w, err)
		return
	}
	h.st.installLocked(inst)
	ok(w)
}

func (st *state) installLocked(inst *instance) *manager.Job {
	return st.job(inst, "install", "Installing "+inst.info.Short, fmt.Sprintf("Downloading the server (%d MB)…", inst.info.DownloadMB), true, func() {
		inst.installed = true
		inst.build = &game.Build{Version: "1.0.3", Date: st.now()}
		st.note(inst, "update", "The "+inst.info.Short+" server was installed.")
	})
}

func (h *handler) stopAll(w http.ResponseWriter, r *http.Request) {
	for _, inst := range h.st.running() {
		h.st.goOffline(inst, true)
	}
	ok(w)
}

// --- adding, removing, stars and the sidebar ----------------------------------------------

func (h *handler) addGame(w http.ResponseWriter, r *http.Request) {
	var in manager.AddGameRequest
	if err := read(r, &in); err != nil {
		fail(w, err)
		return
	}
	st := h.st
	g, found := game.Get(in.Game)
	if !found || g.Info().Stage == game.Hidden {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("There's no game %q to add.", in.Game))
		return
	}
	info := g.Info()
	if st.find(info.ID) != nil {
		writeErr(w, http.StatusBadRequest, info.Short+" is added already.")
		return
	}
	if l, why := info.LevelOn(st.opt.GOOS, st.opt.GOARCH); l == game.CantRun {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s can't be added here: %s.", info.Short, strings.TrimSuffix(why, ".")))
		return
	}
	errs := game.FieldErrors{}
	if strings.TrimSpace(in.ServerName) == "" {
		errs["server.serverName"] = "Give the server a name."
	}
	switch rule := info.Password; {
	case rule.Use == "required" && in.Password == "":
		errs["server.password"] = "Choose a password: friends type it when they join."
	case rule.Use != "none" && in.Password != "" && rule.Min > 0 && len(in.Password) < rule.Min:
		errs["server.password"] = fmt.Sprintf("The password must be at least %d characters.", rule.Min)
	}
	if l, isLicensed := g.(game.Licensed); isLicensed && l.EULA() != "" && !in.EULA {
		errs["eula"] = "Tick the box to accept the licence first."
	}
	switch in.World.Mode {
	case "new":
		if strings.TrimSpace(in.World.Name) == "" {
			errs["world.name"] = "Give the world a name."
		}
	case "import":
		if in.World.Candidate == "" {
			errs["world.candidate"] = "Choose a world from your own game."
		}
	default:
		errs["world.mode"] = "Choose a new world, or one from your own game."
	}
	if len(errs) > 0 {
		fail(w, errs)
		return
	}
	inst, err := st.makeInstance(g)
	if err != nil {
		fail(w, err)
		return
	}
	world := in.World.Name
	if in.World.Mode == "import" {
		world = "My Own World"
	}
	inst.srv.Store().Save(func(s *game.Saved) error {
		if s.Server == nil {
			s.Server = game.Values{}
		}
		for k, v := range in.Values {
			s.Server[k] = v
		}
		s.Server["serverName"], s.World = in.ServerName, world
		if in.Password != "" {
			s.Server["password"] = in.Password
		}
		s.Worlds = map[string]game.Values{world: in.World.Values.Clone()}
		return nil
	})
	inst.worlds = []worlds.World{{Name: world, Format: "folder", NeverSaved: in.World.Mode == "new", Modified: st.now(), Backups: []worlds.Backup{}}}
	inst.orphans = []worlds.Orphan{}
	inst.known = nil
	st.insts = append(st.insts, inst)
	if in.Star {
		st.stars = append(st.stars, inst.id)
	}
	st.note(inst, "update", info.Short+" was added.")
	j := st.installLocked(inst)
	writeJSON(w, http.StatusOK, manager.AddedGame{ID: inst.id, Job: j.ID})
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request, inst *instance) {
	st := h.st
	if err := st.busyLocked(inst); err != nil {
		fail(w, err)
		return
	}
	j := st.job(inst, "remove", "Removing "+inst.info.Short, "Backing up its worlds…", false, func() {
		if inst.state == manager.StateOnline {
			st.goOffline(inst, true)
		}
		st.insts = slices.DeleteFunc(st.insts, func(i *instance) bool { return i == inst })
		st.stars = slices.DeleteFunc(st.stars, func(s string) bool { return s == inst.id })
		st.note(nil, "update", inst.info.Short+" was removed. Its worlds are kept as backups.")
	})
	writeJSON(w, http.StatusOK, map[string]any{"job": j.ID})
}

func (h *handler) star(w http.ResponseWriter, r *http.Request, inst *instance) {
	var body struct {
		On *bool `json:"on"`
	}
	if err := read(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.On == nil {
		writeErr(w, http.StatusBadRequest, `The request didn't say whether to star the game ("on").`)
		return
	}
	h.st.stars = slices.DeleteFunc(h.st.stars, func(s string) bool { return s == inst.id })
	if *body.On {
		h.st.stars = append(h.st.stars, inst.id)
	}
	ok(w)
}

func (h *handler) sidebar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActiveOpen   *bool `json:"activeOpen"`
		InactiveOpen *bool `json:"inactiveOpen"`
	}
	if err := read(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.ActiveOpen != nil {
		h.st.sidebar.ActiveOpen = *body.ActiveOpen
	}
	if body.InactiveOpen != nil {
		h.st.sidebar.InactiveOpen = *body.InactiveOpen
	}
	ok(w)
}

// --- settings -------------------------------------------------------------------------------

func (h *handler) saveSettings(w http.ResponseWriter, r *http.Request, inst *instance) {
	var ch manager.GameSettingsChange
	if err := read(r, &ch); err != nil {
		fail(w, err)
		return
	}
	if ch.AutoStopMinutes != nil && (*ch.AutoStopMinutes < 0 || *ch.AutoStopMinutes > 24*60) {
		fail(w, game.FieldErrors{"autoStopMinutes": "Choose 0 (never) to 1440 minutes."})
		return
	}
	if len(ch.Values) > 0 {
		known := map[string]bool{}
		for _, f := range safely(game.Schema{}, inst.srv.Schema) {
			if f.Scope == game.ServerScope {
				known[f.Key] = true
			}
		}
		errs := game.FieldErrors{}
		for k := range ch.Values {
			if !known[k] {
				errs["server."+k] = "This game has no such setting."
			}
		}
		next := inst.saved()
		for k, v := range ch.Values {
			next.Server[k] = v
		}
		for k, msg := range safely(game.FieldErrors{}, func() game.FieldErrors { return inst.srv.Validate(next, "") }) {
			if _, changed := ch.Values[k]; changed {
				errs["server."+k] = msg
			}
		}
		if len(errs) > 0 {
			fail(w, errs)
			return
		}
		if err := inst.srv.Store().Save(func(s *game.Saved) error {
			for k, v := range ch.Values {
				s.Server[k] = v
			}
			return nil
		}); err != nil {
			fail(w, err)
			return
		}
	}
	if len(ch.Ports) > 0 {
		inst.srv.Store().Save(func(s *game.Saved) error {
			for role, port := range ch.Ports {
				s.Ports[role] = port
				inst.chosen[role] = true
			}
			return nil
		})
	}
	if ch.AutoStopMinutes != nil {
		inst.autoStop = *ch.AutoStopMinutes
	}
	if ch.StartWithApp != nil {
		inst.startWithApp = *ch.StartWithApp
	}
	if ch.Disabled != nil {
		if *ch.Disabled && inst.state == manager.StateOnline {
			h.st.goOffline(inst, true)
		}
		inst.disabled = *ch.Disabled
	}
	if d := ch.Discord; d != nil {
		set := func(to *bool, from *bool) {
			if from != nil {
				*to = *from
			}
		}
		set(&inst.discord.On, d.On)
		set(&inst.discord.Stopped, d.Stopped)
		set(&inst.discord.Daily, d.Daily)
		set(&inst.discord.Password, d.Password)
		if d.Webhook != nil {
			inst.discord.Webhook = *d.Webhook
		}
	}
	ok(w)
}

func (h *handler) pickPorts(w http.ResponseWriter, r *http.Request, inst *instance) {
	ports := map[string]int{}
	inst.srv.Store().Save(func(s *game.Saved) error {
		for _, p := range inst.info.Ports {
			if !inst.chosen[p.Role] {
				s.Ports[p.Role] = p.Default + 10
				ports[p.Role] = p.Default + 10
			}
		}
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ports": ports})
}

func (h *handler) appSettings(w http.ResponseWriter, r *http.Request) {
	var ch manager.AppSettingsChange
	if err := read(r, &ch); err != nil {
		fail(w, err)
		return
	}
	if ch.IdleMinutes != nil && (*ch.IdleMinutes < 10 || *ch.IdleMinutes > 1440) {
		fail(w, config.FieldErrors{"idleMinutes": "Choose between 10 and 1440 minutes."})
		return
	}
	for _, f := range []struct{ to, from *bool }{
		{h.st.app.IdleHibernate, ch.IdleHibernate}, {h.st.app.AutoUpdate, ch.AutoUpdate},
		{h.st.app.StartAfterWake, ch.StartAfterWake}, {h.st.app.RouterPorts, ch.RouterPorts},
	} {
		if f.from != nil {
			*f.to = *f.from
		}
	}
	if ch.IdleMinutes != nil {
		*h.st.app.IdleMinutes = *ch.IdleMinutes
	}
	ok(w)
}

// oldSettings is GET /api/settings: the legacy game's settings.json and the
// app-wide settings in it, as the app answers it.
func (h *handler) oldSettings(w http.ResponseWriter, r *http.Request) {
	s := config.Defaults()
	for _, inst := range h.st.insts {
		if inst.info.Legacy {
			saved := inst.saved()
			s.ServerName, s.Password, s.World = saved.Server["serverName"], saved.Server["password"], saved.World
		}
	}
	s.IdleHibernate, s.IdleMinutes = *h.st.app.IdleHibernate, *h.st.app.IdleMinutes
	s.AutoUpdate, s.StartAfterWake = *h.st.app.AutoUpdate, *h.st.app.StartAfterWake
	s.DiscordWebhook = h.st.mainWebhook
	writeJSON(w, http.StatusOK, map[string]any{"settings": s, "presets": config.Presets, "modifiers": config.Modifiers,
		"modifierOrder": config.ModifierOrder, "keys": config.Keys, "paths": config.ResolvePaths(h.st.opt.Dir, s),
		"launchOnLogin": false, "version": "dev fixtures"})
}

// --- Discord and the router ------------------------------------------------------------------

func webhookProblem(link string) string {
	if !strings.HasPrefix(strings.TrimSpace(link), "https://discord.com/api/webhooks/") {
		return "That isn't a webhook link. In Discord: channel settings, Integrations, Webhooks, Copy Webhook URL."
	}
	return ""
}

func (h *handler) mainDiscord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Webhook *string `json:"webhook"`
	}
	if err := read(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.Webhook == nil {
		writeErr(w, http.StatusBadRequest, `The request has no webhook link ("webhook").`)
		return
	}
	if *body.Webhook != "" {
		if msg := webhookProblem(*body.Webhook); msg != "" {
			fail(w, config.FieldErrors{"discordWebhook": msg})
			return
		}
	}
	h.st.mainWebhook = *body.Webhook
	ok(w)
}

func (h *handler) testDiscord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Webhook string `json:"webhook"`
		Game    string `json:"game"`
	}
	if err := read(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.Game != "" && h.st.find(body.Game) == nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("There's no game %q on this computer.", body.Game))
		return
	}
	if msg := webhookProblem(body.Webhook); msg != "" {
		fail(w, config.FieldErrors{"discordWebhook": msg})
		return
	}
	ok(w) // nothing is posted
}

func (h *handler) router(w http.ResponseWriter, r *http.Request) {
	v := manager.RouterView{Kind: "upnp", ExternalIP: publicIP, PublicIP: publicIP, Gateway: "192.168.1.1",
		LAN: []string{lanIP}, Mappings: []manager.RouterMapping{}, Steam: []manager.SteamSeen{}}
	for _, inst := range h.st.running() {
		saved := inst.saved()
		for _, p := range inst.info.Ports {
			if !p.Public {
				continue
			}
			for _, proto := range p.Protos {
				v.Mappings = append(v.Mappings, manager.RouterMapping{Instance: inst.id, Role: p.Role, Proto: proto, Port: saved.Ports[p.Role], State: "open"})
			}
		}
		v.Steam = append(v.Steam, manager.SteamSeen{Instance: inst.id, Seen: true})
	}
	writeJSON(w, http.StatusOK, v)
}

// --- worlds ------------------------------------------------------------------------------------

func worldIndex(inst *instance, name string) int {
	return slices.IndexFunc(inst.worlds, func(w worlds.World) bool { return strings.EqualFold(w.Name, name) })
}

type nameBody struct {
	Name     string      `json:"name"`
	NewName  string      `json:"newName"`
	BackupID string      `json:"backupId"`
	Restart  bool        `json:"restart"`
	Values   game.Values `json:"values"`
}

func (h *handler) world(w http.ResponseWriter, r *http.Request, inst *instance) (nameBody, int, bool) {
	var b nameBody
	if err := read(r, &b); err != nil {
		fail(w, err)
		return b, -1, false
	}
	i := worldIndex(inst, b.Name)
	if i < 0 {
		writeErr(w, http.StatusBadRequest, "There's no world with that name.")
		return b, -1, false
	}
	return b, i, true
}

func (h *handler) selectWorld(w http.ResponseWriter, r *http.Request, inst *instance) {
	_, i, found := h.world(w, r, inst)
	if !found {
		return
	}
	inst.srv.Store().Save(func(s *game.Saved) error { s.World = inst.worlds[i].Name; return nil })
	ok(w)
}

func (h *handler) createWorld(w http.ResponseWriter, r *http.Request, inst *instance) {
	var b nameBody
	if err := read(r, &b); err != nil {
		fail(w, err)
		return
	}
	name := strings.TrimSpace(b.Name)
	if msg := safely("", func() string { return inst.srv.Worlds().NameProblem(name) }); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if worldIndex(inst, name) >= 0 {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("there's already a world called %q", name))
		return
	}
	inst.worlds = append(inst.worlds, worlds.World{Name: name, Format: "folder", NeverSaved: true, Modified: h.st.now(), Backups: []worlds.Backup{}})
	inst.srv.Store().Save(func(s *game.Saved) error {
		if s.Worlds == nil {
			s.Worlds = map[string]game.Values{}
		}
		s.Worlds[name], s.World = b.Values.Clone(), name
		return nil
	})
	ok(w)
}

func (h *handler) deleteWorld(w http.ResponseWriter, r *http.Request, inst *instance) {
	b, i, found := h.world(w, r, inst)
	if !found {
		return
	}
	switch {
	case strings.EqualFold(inst.saved().World, b.Name):
		writeErr(w, http.StatusBadRequest, "This is the selected world. Pick a different world to play before deleting it.")
		return
	case inst.state == manager.StateOnline && strings.EqualFold(inst.runWorld, b.Name):
		writeErr(w, http.StatusBadRequest, "Stop the server first: this world is being played.")
		return
	}
	gone := inst.worlds[i]
	h.st.job(inst, "delete", "Deleting "+gone.Name, "Keeping a backup copy, then deleting…", false, func() {
		if j := worldIndex(inst, gone.Name); j >= 0 {
			inst.worlds = slices.Delete(inst.worlds, j, j+1)
			backups := append(gone.Backups, worlds.Backup{ID: "deleted-" + strconv.Itoa(h.st.jobSeq), Kind: "deleted",
				Label: "Before it was deleted", Time: h.st.now(), Size: gone.Size, Format: "zip"})
			inst.orphans = append(inst.orphans, worlds.Orphan{Name: gone.Name, Backups: backups})
		}
	})
	ok(w)
}

func (h *handler) backupWorld(w http.ResponseWriter, r *http.Request, inst *instance) {
	b, _, found := h.world(w, r, inst)
	if !found {
		return
	}
	h.st.job(inst, "backup", "Backing up "+b.Name, "Copying the world into a backup file…", false, func() {
		if i := worldIndex(inst, b.Name); i >= 0 {
			inst.worlds[i].Backups = append([]worlds.Backup{{ID: "manual-" + strconv.Itoa(h.st.jobSeq), Kind: "manual",
				Label: "Backup", Time: h.st.now(), Size: inst.worlds[i].Size, Format: "zip"}}, inst.worlds[i].Backups...)
		}
	})
	ok(w)
}

func (h *handler) restoreWorld(w http.ResponseWriter, r *http.Request, inst *instance) {
	var b nameBody
	if err := read(r, &b); err != nil {
		fail(w, err)
		return
	}
	if inst.state == manager.StateOnline && strings.EqualFold(inst.runWorld, b.Name) {
		writeErr(w, http.StatusBadRequest, "Stop the server first: this world is being played.")
		return
	}
	h.st.job(inst, "restore", "Restoring "+b.Name, "Keeping a copy of the current world, then restoring the backup…", false, func() {
		if i := slices.IndexFunc(inst.orphans, func(o worlds.Orphan) bool { return o.Name == b.Name }); i >= 0 {
			inst.worlds = append(inst.worlds, worlds.World{Name: b.Name, Format: "folder", Modified: h.st.now(), Backups: inst.orphans[i].Backups})
			inst.orphans = slices.Delete(inst.orphans, i, i+1)
		}
	})
	ok(w)
}

func (h *handler) renameWorld(w http.ResponseWriter, r *http.Request, inst *instance) {
	b, i, found := h.world(w, r, inst)
	if !found {
		return
	}
	newName := strings.TrimSpace(b.NewName)
	if msg := safely("", func() string { return inst.srv.Worlds().NameProblem(newName) }); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	old := inst.worlds[i].Name
	inst.worlds[i].Name = newName
	inst.srv.Store().Save(func(s *game.Saved) error {
		if s.World == old {
			s.World = newName
		}
		if v, has := s.Worlds[old]; has {
			s.Worlds[newName] = v
			delete(s.Worlds, old)
		}
		return nil
	})
	ok(w)
}

func (h *handler) worldValues(w http.ResponseWriter, r *http.Request, inst *instance) {
	b, i, found := h.world(w, r, inst)
	if !found {
		return
	}
	if b.Values == nil {
		writeErr(w, http.StatusBadRequest, `The request has no world settings to save ("values").`)
		return
	}
	name := inst.worlds[i].Name
	inst.srv.Store().Save(func(s *game.Saved) error {
		if s.Worlds == nil {
			s.Worlds = map[string]game.Values{}
		}
		cur := s.Worlds[name].Clone()
		if cur == nil {
			cur = game.Values{}
		}
		for k, v := range b.Values {
			cur[k] = v
		}
		s.Worlds[name] = cur
		return nil
	})
	ok(w)
}

// --- uploads and the owner's own worlds ------------------------------------------------------

func (st *state) localCandidates(info game.Info) []worlds.Candidate {
	if !info.Import {
		return []worlds.Candidate{}
	}
	return []worlds.Candidate{{ID: "local-1", Name: "My Own World", Format: "folder", Size: 31 << 20,
		Modified: st.now().AddDate(0, 0, -3), Renamable: true}}
}

func (h *handler) catalogLocal(w http.ResponseWriter, r *http.Request) {
	g, found := game.Get(r.PathValue("game"))
	if !found {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("There's no game %q to add.", r.PathValue("game")))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": h.st.localCandidates(g.Info())})
}

func (h *handler) newUpload(w http.ResponseWriter, r *http.Request, inst *instance) {
	h.st.upSeq++
	id := fmt.Sprintf("up%d", h.st.upSeq)
	h.st.uploads[id] = inst.id
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *handler) uploadFiles(w http.ResponseWriter, r *http.Request) {
	h.st.mu.Lock()
	_, known := h.st.uploads[r.PathValue("upload")]
	h.st.mu.Unlock()
	if !known {
		writeErr(w, http.StatusBadRequest, "That upload has expired. Please upload the world again.")
		return
	}
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
		io.Copy(io.Discard, part)
		part.Close()
		count++
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": count})
}

func (h *handler) scanUpload(w http.ResponseWriter, r *http.Request, inst *instance) {
	if _, known := h.st.uploads[r.PathValue("upload")]; !known {
		writeErr(w, http.StatusBadRequest, "That upload has expired. Please upload the world again.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": []worlds.Candidate{{ID: "c1", Name: "Uploaded World",
		Format: "folder", Size: 9 << 20, Modified: h.st.now().Add(-time.Hour), Renamable: true}}})
}

func (h *handler) importWorlds(w http.ResponseWriter, r *http.Request, inst *instance) {
	var body struct {
		Choices []worlds.Choice `json:"choices"`
	}
	if err := read(r, &body); err != nil {
		fail(w, err)
		return
	}
	var add []string
	for _, c := range body.Choices {
		if c.Action == "import" || c.Action == "replace" {
			name := c.Name
			if name == "" {
				name = "Imported World"
			}
			add = append(add, name)
		}
	}
	if len(add) == 0 {
		writeErr(w, http.StatusBadRequest, "Choose at least one world to import.")
		return
	}
	j := h.st.job(inst, "import", "Importing worlds", "Copying the worlds…", false, func() {
		for _, name := range add {
			if worldIndex(inst, name) < 0 {
				inst.worlds = append(inst.worlds, worlds.World{Name: name, Format: "folder", Size: 9 << 20, Modified: h.st.now(), Backups: []worlds.Backup{}})
			}
		}
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": j.ID})
}

// --- players, lists, stats and the log -------------------------------------------------------

func (h *handler) records(w http.ResponseWriter, r *http.Request, inst *instance) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "all"
	}
	sum, err := h.st.recordsLocked(inst, period)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (h *handler) setList(w http.ResponseWriter, r *http.Request, inst *instance) {
	var body struct {
		Kind string    `json:"kind"`
		IDs  *[]string `json:"ids"`
	}
	if err := read(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.Kind == "" || body.IDs == nil {
		writeErr(w, http.StatusBadRequest, `The request didn't say which list ("kind") or who is on it ("ids").`)
		return
	}
	if _, has := inst.lists[body.Kind]; !has {
		writeErr(w, http.StatusBadRequest, "unknown list")
		return
	}
	inst.lists[body.Kind] = append([]string{}, *body.IDs...)
	ok(w)
}

func (h *handler) allow(w http.ResponseWriter, r *http.Request, inst *instance) {
	var body struct {
		Name string `json:"name"`
	}
	if err := read(r, &body); err != nil {
		fail(w, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "Say which player to allow.")
		return
	}
	if _, has := inst.lists["allowed"]; !has {
		writeErr(w, http.StatusBadRequest, inst.info.Short+" has no allow list.")
		return
	}
	if !slices.Contains(inst.lists["allowed"], name) {
		inst.lists["allowed"] = append(inst.lists["allowed"], name)
	}
	h.st.note(inst, "allow", name+" can join "+inst.info.Short+" now.")
	ok(w)
}

var ranges = map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour}

func (h *handler) stats(w http.ResponseWriter, r *http.Request, inst *instance) {
	d, known := ranges[r.URL.Query().Get("range")]
	if !known {
		d = time.Hour
	}
	samples := h.st.samplesLocked(inst, d)
	running := 0.0
	for _, s := range samples {
		if s.Running {
			running += 10
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": samples, "runningSeconds": min(running, d.Seconds()),
		"rangeSeconds": d.Seconds(), "byPlayers": h.st.byPlayersLocked(inst),
		"machine": h.st.machine()})
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request, inst *instance) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	lines := []manager.LogLine{}
	for _, l := range inst.logs {
		if l.Seq > after {
			lines = append(lines, l)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "last": inst.logSeq})
}

func (h *handler) downloadLog(w http.ResponseWriter, r *http.Request, inst *instance) {
	if len(inst.logs) == 0 {
		writeErr(w, http.StatusNotFound, "There's no server log yet.")
		return
	}
	var b strings.Builder
	for _, l := range inst.logs {
		fmt.Fprintf(&b, "%s %s\n", l.Time.Format("2006-01-02 15:04:05"), l.Text)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": inst.id + ".log"}))
	io.WriteString(w, b.String())
}
