package web

// The new panel's routes for one game (docs/dev/multigame/spec.md 6.2):
// everything under /api/games/{id}/, where {id} is an instance (this
// round, the game's own id). Each route asks the manager's …Game method
// for that instance. For the legacy game (Valheim) those methods call the
// ones its old routes call, so Valheim answers the same way through both;
// the old routes (api.go) stay, bound to the legacy game, for the old
// panel and for a page that is open while the app updates.
//
// Answers have the old routes' shapes: {ok: true} for an action, and
// {error, fields?, action?, data?} with 400 (409 when another job is in
// the way) when it can't be done (fail, in api.go).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"valheim-easy-server/internal/config"
	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/manager"
	"valheim-easy-server/internal/worlds"
)

// route is one of the new routes: a ServeMux pattern ("POST /api/…") and
// its handler.
type route struct {
	pattern string
	handler http.HandlerFunc
}

// newRoutes are the routes the new panel adds. They are kept as a list
// rather than registered one by one, so the tests can try every one of
// them (that guard refuses each change asked without the panel's header,
// for one).
func (s *Server) newRoutes() []route {
	return append(s.gameRoutes(), s.appRoutes()...)
}

// gameRoutes are one game's routes.
func (s *Server) gameRoutes() []route {
	return []route{
		{"POST /api/games/{id}/start", s.startGame},
		{"POST /api/games/{id}/stop", s.gameAction(s.m.StopGame)},
		{"POST /api/games/{id}/restart", s.gameAction(s.m.RestartGame)},
		{"POST /api/games/{id}/force-stop", s.gameAction(s.m.ForceStopGame)},
		{"POST /api/games/{id}/cancel", s.gameAction(s.m.CancelGameJob)},
		{"POST /api/games/{id}/install", s.installGame},
		{"POST /api/games/{id}/remove", s.removeGame},
		{"POST /api/games/{id}/star", s.starGame},
		{"POST /api/games/{id}/keep-on", s.gameAction(s.m.KeepOn)},

		{"GET /api/games/{id}/settings", s.gameSettings},
		{"POST /api/games/{id}/settings", s.saveGameSettings},
		{"POST /api/games/{id}/ports/pick", s.pickPorts},

		{"GET /api/games/{id}/worlds", s.gameWorlds},
		{"POST /api/games/{id}/worlds/select", s.selectGameWorld},
		{"POST /api/games/{id}/worlds/create", s.createGameWorld},
		{"POST /api/games/{id}/worlds/delete", s.gameWorldAction(s.m.DeleteWorldGame)},
		{"POST /api/games/{id}/worlds/backup", s.gameWorldAction(s.m.BackupWorldGame)},
		{"POST /api/games/{id}/worlds/restore", s.restoreGameWorld},
		{"POST /api/games/{id}/worlds/rename", s.renameGameWorld},
		{"POST /api/games/{id}/worlds/values", s.gameWorldValues},
		{"GET /api/games/{id}/worlds/download", s.downloadGameWorld},
		{"GET /api/games/{id}/worlds/backup-download", s.downloadGameBackup},

		{"POST /api/games/{id}/uploads", s.newGameUpload},
		{"POST /api/games/{id}/uploads/{upload}/files", s.gameUploadFiles},
		{"POST /api/games/{id}/uploads/{upload}/scan", s.scanGameUpload},
		{"POST /api/games/{id}/uploads/{upload}/import", s.importGameUpload},
		{"DELETE /api/games/{id}/uploads/{upload}", s.discardGameUpload},
		{"GET /api/games/{id}/local-worlds", s.gameLocalWorlds},
		{"POST /api/games/{id}/local-worlds/import", s.importGameLocal},

		{"GET /api/games/{id}/players", s.gamePlayers},
		{"GET /api/games/{id}/records", s.gameRecords},
		{"GET /api/games/{id}/lists", s.gameLists},
		{"POST /api/games/{id}/lists", s.saveGameList},
		{"POST /api/games/{id}/allow", s.allowPlayer},

		{"GET /api/games/{id}/stats", s.gameStats},
		{"POST /api/games/{id}/stats/reset", s.gameAction(s.m.ResetStatsGame)},
		{"GET /api/games/{id}/logs", s.gameLogs},
		{"GET /api/games/{id}/logs/download", s.downloadGameLog},
	}
}

// --- reading requests ---------------------------------------------------------

// maxBody is the most a JSON request may hold (uploads are streamed and
// have their own limit).
const maxBody = 1 << 20

// readBody reads a new route's JSON body into v. An empty body counts as
// {}, so a route whose fields are all optional ("{}" in the spec) can be
// called without one.
//
// Every setting is text (game.Values: "25565", "true"), but a form
// naturally sends a number box as a JSON number and a checkbox as true or
// false. So inside any "values" object, numbers and true/false are taken
// as the text they are written as, rather than refused.
func readBody(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("the request couldn't be read: %v", err)
	}
	if len(b) > maxBody {
		return errors.New("The request is too big.")
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if b, err = valuesAsText(b); err == nil {
		err = json.Unmarshal(b, v)
	}
	if err != nil {
		return fmt.Errorf("the request couldn't be read: %v", err)
	}
	return nil
}

// valuesAsText rewrites a JSON document so that the numbers and booleans
// in its "values" objects (at any depth) become strings written the same
// way. Everything else is left as it is.
func valuesAsText(b []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // keeps each number exactly as it was written
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	changed := false
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if vals, ok := c.(map[string]any); ok && k == "values" {
					for key, val := range vals {
						switch t := val.(type) {
						case json.Number:
							vals[key], changed = t.String(), true
						case bool:
							vals[key], changed = strconv.FormatBool(t), true
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
	if !changed {
		return b, nil
	}
	return json.Marshal(doc)
}

// --- starting and stopping -------------------------------------------------------

// gameAction is an action on a game that takes nothing ({} or no body).
func (s *Server) gameAction(fn func(id string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		ok(w)
	}
}

// startGame starts a game: {memory} is "" to check the free memory first,
// "anyway" or "stop:<instance>" (the memory warning's buttons, spec 6.1).
// A start that doesn't fit answers action "memory" with the warning's
// data.
func (s *Server) startGame(w http.ResponseWriter, r *http.Request) {
	var o manager.StartOptions
	if err := readBody(r, &o); err != nil {
		fail(w, err)
		return
	}
	o.Source = manager.SourcePanel // never taken from the request
	if err := s.m.StartGame(r.PathValue("id"), o); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) installGame(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Repair bool `json:"repair"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.InstallGame(r.PathValue("id"), body.Repair); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// removeGame removes a game (for the legacy game: hides it) as a job.
func (s *Server) removeGame(w http.ResponseWriter, r *http.Request) {
	job, err := s.m.RemoveGame(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

// starGame stars or unstars a game: {on}. A request that doesn't say which
// is refused rather than read as "unstar".
func (s *Server) starGame(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.On == nil {
		writeErr(w, http.StatusBadRequest, `The request didn't say whether to star the game ("on").`)
		return
	}
	if err := s.m.SetStar(r.PathValue("id"), *body.On); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// --- settings ---------------------------------------------------------------------

func (s *Server) gameSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.m.GameSettings(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if v.Schema == nil {
		v.Schema = game.Schema{}
	}
	if v.Values.Server == nil {
		v.Values.Server = game.Values{}
	}
	if v.Values.Worlds == nil {
		v.Values.Worlds = map[string]game.Values{}
	}
	if v.Ports == nil {
		v.Ports = []manager.PortSetting{}
	}
	writeJSON(w, http.StatusOK, v)
}

// saveGameSettings saves the parts of a game's Settings tab that the
// request has, and only those: {values?, ports?, autoStopMinutes?,
// startWithApp?, disabled?, discord?, eula?}. A part that is left out
// keeps what it is, so a page that is out of date never blanks a setting
// someone changed elsewhere; a part that is there, even false or 0, is
// saved. The manager merges the change into what is saved.
func (s *Server) saveGameSettings(w http.ResponseWriter, r *http.Request) {
	var ch manager.GameSettingsChange
	if err := readBody(r, &ch); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.UpdateGameSettings(r.PathValue("id"), ch); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// pickPorts picks free ports for every port the owner didn't choose, and
// answers them by role.
func (s *Server) pickPorts(w http.ResponseWriter, r *http.Request) {
	ports, err := s.m.PickPorts(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if ports == nil {
		ports = map[string]int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ports": ports})
}

// --- worlds -------------------------------------------------------------------------

// isLegacy reports whether an instance is the legacy game's. Its instance
// id is the game's own (there is only one, kept in the legacy files), and
// it is found through the registry, so no game is named here.
func isLegacy(id string) bool {
	g := game.Legacy()
	return g != nil && g.Info().ID == id
}

// worldNameProblem is the check a world route makes on the name of a
// world before it asks the manager, or "" when it may go on.
//
// For the legacy game it is Valheim's own rule, on the routes whose old
// form checks it (oldChecks), so the new routes refuse for Valheim exactly
// what the old ones do (for the other routes the manager checks the name,
// as it does for the old ones). Every other game has a rule of its own,
// which its adapter applies; here only a name that could never be a world
// in a folder is refused: nothing at all, or a path.
func worldNameProblem(id, name string, oldChecks bool) string {
	if isLegacy(id) {
		if oldChecks {
			return config.WorldNameProblem(name)
		}
		return ""
	}
	switch {
	case strings.TrimSpace(name) == "":
		return "Give the world a name."
	case strings.ContainsAny(name, `/\`):
		return `A world name can't contain / or \.`
	case strings.ContainsFunc(name, unicode.IsControl):
		return "A world name can't contain control characters."
	case name == "." || name == "..":
		return "A world name can't be only dots."
	case len(name) > 255:
		return "That world name is too long."
	}
	return ""
}

func (s *Server) gameWorlds(w http.ResponseWriter, r *http.Request) {
	v, err := s.m.WorldsGame(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if v.Worlds == nil {
		v.Worlds = []worlds.World{}
	}
	if v.Orphans == nil {
		v.Orphans = []worlds.Orphan{}
	}
	if v.Schema.World == nil {
		v.Schema.World = game.Schema{}
	}
	if v.Schema.NewWorld == nil {
		v.Schema.NewWorld = game.Schema{}
	}
	if v.Values == nil {
		v.Values = map[string]game.Values{}
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) selectGameWorld(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Restart bool   `json:"restart"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	id := r.PathValue("id")
	if msg := worldNameProblem(id, body.Name, false); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.m.SelectWorldGame(id, body.Name, body.Restart); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// createGameWorld makes a world from its new-world values ({name, values,
// restart}); the server makes its files at its first start. Its name is
// checked by the game's own rule in the manager.
func (s *Server) createGameWorld(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string      `json:"name"`
		Values  game.Values `json:"values"`
		Restart bool        `json:"restart"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.Values == nil {
		body.Values = game.Values{}
	}
	if err := s.m.CreateWorldGame(r.PathValue("id"), body.Name, body.Values, body.Restart); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// gameWorldAction is an action on one world that takes only its name:
// {name}.
func (s *Server) gameWorldAction(fn func(id, name string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := readBody(r, &body); err != nil {
			fail(w, err)
			return
		}
		id := r.PathValue("id")
		if msg := worldNameProblem(id, body.Name, true); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		if err := fn(id, body.Name); err != nil {
			fail(w, err)
			return
		}
		ok(w)
	}
}

func (s *Server) restoreGameWorld(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		BackupID string `json:"backupId"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	id := r.PathValue("id")
	if msg := worldNameProblem(id, body.Name, true); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.m.RestoreWorldGame(id, body.Name, body.BackupID); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) renameGameWorld(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		NewName string `json:"newName"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	id := r.PathValue("id")
	// The old name isn't held to the game's rule: renaming is how a world
	// with a name the rule no longer allows gets a good one.
	if msg := worldNameProblem(id, body.Name, false); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.m.RenameWorldGame(id, body.Name, body.NewName); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// gameWorldValues saves a world's world-scope values: {name, values}. Only
// the values that are there change; a request without values is refused
// rather than read as "no values".
func (s *Server) gameWorldValues(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string      `json:"name"`
		Values game.Values `json:"values"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	id := r.PathValue("id")
	if msg := worldNameProblem(id, body.Name, true); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if body.Values == nil {
		writeErr(w, http.StatusBadRequest, `The request has no world settings to save ("values").`)
		return
	}
	if err := s.m.SetWorldValuesGame(id, body.Name, body.Values); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// downloadGameWorld sends a world as a zip (?name=).
func (s *Server) downloadGameWorld(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.URL.Query().Get("name")
	if msg := worldNameProblem(id, name, true); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	path, filename, err := s.m.PrepareDownloadGame(id, name)
	sendWorldZip(w, r, path, filename, err)
}

// downloadGameBackup sends one of a world's backups as a zip
// (?name=&id=).
func (s *Server) downloadGameBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name, backupID := r.URL.Query().Get("name"), r.URL.Query().Get("id")
	if msg := worldNameProblem(id, name, true); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	s.sendBackup(w, name, backupID, func(out io.Writer) error { return s.m.ExportBackupGame(id, out, name, backupID) })
}

// --- uploads and the owner's own worlds --------------------------------------------

func (s *Server) newGameUpload(w http.ResponseWriter, r *http.Request) {
	u, err := s.m.NewUploadGame(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": u.ID})
}

func (s *Server) gameUploadFiles(w http.ResponseWriter, r *http.Request) {
	receiveFiles(w, r, func() (*worlds.Upload, error) { return s.m.UploadGame(r.PathValue("id"), r.PathValue("upload")) })
}

func (s *Server) scanGameUpload(w http.ResponseWriter, r *http.Request) {
	cands, err := s.m.FinishUploadGame(r.PathValue("id"), r.PathValue("upload"))
	if err != nil {
		fail(w, err)
		return
	}
	writeCandidates(w, cands)
}

// writeCandidates answers the worlds found to import: {candidates}.
func writeCandidates(w http.ResponseWriter, cands []worlds.Candidate) {
	if cands == nil {
		cands = []worlds.Candidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": cands})
}

func (s *Server) importGameUpload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Choices []worlds.Choice `json:"choices"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	job, err := s.m.ImportUploadGame(r.PathValue("id"), r.PathValue("upload"), body.Choices)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": job})
}

func (s *Server) discardGameUpload(w http.ResponseWriter, r *http.Request) {
	if err := s.m.DiscardUploadGame(r.PathValue("id"), r.PathValue("upload")); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

func (s *Server) gameLocalWorlds(w http.ResponseWriter, r *http.Request) {
	cands, err := s.m.LocalWorldsGame(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeCandidates(w, cands)
}

func (s *Server) importGameLocal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Choices []worlds.Choice `json:"choices"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	job, err := s.m.ImportLocalGame(r.PathValue("id"), body.Choices)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": job})
}

// --- players and lists ----------------------------------------------------------------

func (s *Server) gamePlayers(w http.ResponseWriter, r *http.Request) {
	players, err := s.m.KnownPlayersGame(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if players == nil {
		players = []manager.KnownPlayer{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"players": players})
}

// gameRecords are for ?period= "today", "week" or "all" (the default).
func (s *Server) gameRecords(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "all"
	}
	sum, err := s.m.RecordsGame(r.PathValue("id"), period)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

// gameLists are a game's player lists: [{kind, ids}], none for a game
// without them.
func (s *Server) gameLists(w http.ResponseWriter, r *http.Request) {
	lists, err := s.m.ListsGame(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if lists == nil {
		lists = []manager.GameList{}
	}
	writeJSON(w, http.StatusOK, lists)
}

// saveGameList replaces one of a game's player lists: {kind, ids}. A
// request without ids is refused, so a mistake never empties a list; an
// empty list ("ids": []) does.
func (s *Server) saveGameList(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string    `json:"kind"`
		IDs  *[]string `json:"ids"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.Kind == "" || body.IDs == nil {
		writeErr(w, http.StatusBadRequest, `The request didn't say which list ("kind") or who is on it ("ids").`)
		return
	}
	if err := s.m.SetListGame(r.PathValue("id"), body.Kind, *body.IDs); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// allowPlayer adds a player who was turned away to the game's allow list,
// with one click: {name}.
func (s *Server) allowPlayer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "Say which player to allow.")
		return
	}
	if err := s.m.AllowPlayer(r.PathValue("id"), name); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// --- stats and logs --------------------------------------------------------------------

// gameStats answers a game's Stats page (?range=), in the shape of the old
// one.
func (s *Server) gameStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d := statsRange(r)
	now := time.Now()
	raw, err := s.m.HistoryGame(id, now.Add(-d))
	if err != nil {
		fail(w, err)
		return
	}
	byPlayers, err := s.m.ByPlayersGame(id)
	if err != nil {
		fail(w, err)
		return
	}
	s.writeStats(w, now, d, raw, byPlayers)
}

// gameLogs are the game's server log lines after ?after=.
func (s *Server) gameLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	lines, last, err := s.m.LogsGame(r.PathValue("id"), after, 1000)
	if err != nil {
		fail(w, err)
		return
	}
	writeLogs(w, lines, last)
}

func (s *Server) downloadGameLog(w http.ResponseWriter, r *http.Request) {
	path, err := s.m.CurrentLogPathGame(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	sendLog(w, path)
}
