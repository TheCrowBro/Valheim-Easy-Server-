package web

// The new panel's routes that aren't one game's (docs/dev/multigame/spec.md
// 6.2): Add a game (the catalog of games this build can run, and adding
// one), the sidebar, the App settings, Discord and router pages, and
// stopping every game.

import (
	"net/http"

	"valheim-easy-server/internal/manager"
)

func (s *Server) appRoutes() []route {
	return []route{
		{"GET /api/catalog", s.catalog},
		{"GET /api/catalog/{game}/local", s.catalogLocal},
		{"POST /api/games", s.addGame},

		{"POST /api/app/sidebar", s.saveSidebar},
		{"POST /api/app/settings", s.saveAppSettings},
		{"POST /api/discord/main", s.saveMainDiscord},
		{"GET /api/router", s.router},
		{"POST /api/router/check", s.act(func(*http.Request) error { return s.m.CheckRouter() })},
		{"POST /api/power/stop-all", s.act(func(*http.Request) error { return s.m.StopAll("asked on the control panel") })},
	}
}

// catalog is every game Add a game shows: [{id, name, …, added}].
func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	games := s.m.Catalog()
	if games == nil {
		games = []manager.CatalogGame{}
	}
	writeJSON(w, http.StatusOK, games)
}

// catalogLocal lists the worlds in the owner's own copy of a game that
// isn't added yet, for Add a game's "From your own game": {candidates}.
func (s *Server) catalogLocal(w http.ResponseWriter, r *http.Request) {
	cands, err := s.m.CatalogLocal(r.PathValue("game"))
	if err != nil {
		fail(w, err)
		return
	}
	writeCandidates(w, cands)
}

// addGame adds a game and starts installing it: {game, serverName,
// password, values?, eula, star, world: {mode, name, values, candidate}}
// answers {id, job}. "Star it" is only what the request says (unticked in
// the panel by default, as the owner decided).
func (s *Server) addGame(w http.ResponseWriter, r *http.Request) {
	var req manager.AddGameRequest
	if err := readBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	added, err := s.m.AddGame(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, added)
}

// saveSidebar remembers which of the sidebar's lists are open:
// {activeOpen, inactiveOpen}. A list the request leaves out stays as it
// is, so opening one list never closes the other.
func (s *Server) saveSidebar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActiveOpen   *bool `json:"activeOpen"`
		InactiveOpen *bool `json:"inactiveOpen"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.ActiveOpen == nil && body.InactiveOpen == nil {
		ok(w)
		return
	}
	cur := s.m.Status().Sidebar
	if body.ActiveOpen != nil {
		cur.ActiveOpen = *body.ActiveOpen
	}
	if body.InactiveOpen != nil {
		cur.InactiveOpen = *body.InactiveOpen
	}
	if err := s.m.SetSidebar(cur.ActiveOpen, cur.InactiveOpen); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// saveAppSettings saves the App settings page's parts that the request
// has: {idleHibernate?, idleMinutes?, autoUpdate?, startAfterWake?,
// routerPorts?}. What it leaves out stays as it is.
func (s *Server) saveAppSettings(w http.ResponseWriter, r *http.Request) {
	var ch manager.AppSettingsChange
	if err := readBody(r, &ch); err != nil {
		fail(w, err)
		return
	}
	if err := s.m.UpdateAppSettings(ch); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// saveMainDiscord saves the main Discord channel, where every game without
// a channel of its own posts: {webhook}, where "" turns it off. A request
// without "webhook" is refused rather than read as "turn it off".
func (s *Server) saveMainDiscord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Webhook *string `json:"webhook"`
	}
	if err := readBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	if body.Webhook == nil {
		writeErr(w, http.StatusBadRequest, `The request has no webhook link ("webhook").`)
		return
	}
	if err := s.m.SetMainDiscord(*body.Webhook); err != nil {
		fail(w, err)
		return
	}
	ok(w)
}

// router is what the app knows about the router and the way in from the
// internet.
func (s *Server) router(w http.ResponseWriter, r *http.Request) {
	v := s.m.Router()
	if v.LAN == nil {
		v.LAN = []string{}
	}
	if v.Mappings == nil {
		v.Mappings = []manager.RouterMapping{}
	}
	if v.Steam == nil {
		v.Steam = []manager.SteamSeen{}
	}
	writeJSON(w, http.StatusOK, v)
}
