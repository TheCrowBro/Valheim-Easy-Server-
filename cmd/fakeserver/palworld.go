package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The Palworld personality stands in for the Palworld 1.0 dedicated server
// (spec 5.9), as the app sees it (internal/games/palworld/FACTS.md has the
// real server's facts and their sources):
//
//   - It is started as Pal/Binaries/Win64/PalServer-Win64-Shipping-Cmd(.exe)
//     with "Pal -port=8211 -players=32", and finds the install from its own
//     place: the folder three levels up.
//   - It reads Pal/Saved/Config/WindowsServer/PalWorldSettings.ini's
//     OptionSettings line (the REST API's port and switch, AdminPassword,
//     ServerName, ServerPassword, AutoSaveSpan, bIsUseBackupSaveData) and
//     GameUserSettings.ini's DedicatedServerName. With no name it makes a
//     new 32-character id and writes it there, as the real one does.
//   - Its world is Pal/Saved/SaveGames/0/<id>/: Level.sav, LevelMeta.sav,
//     Players/<player id>.sav, and its own rolling copies in backup/world/.
//     A new world's folder appears at its first save, as the real one's
//     does; it autosaves every AutoSaveSpan seconds (30 by default).
//   - It listens on the game port (UDP) and serves the REST API under
//     /v1/api/ with basic authentication ("admin", AdminPassword; anything
//     else gets 401): GET info, players, metrics; POST save, shutdown (which
//     needs {"waittime": N}, then saves and exits N seconds later), announce,
//     stop (exits at once without saving).
//   - Ctrl+C ends it without saving: the real server's behaviour on Ctrl+C
//     is still to be confirmed, and assuming no save is the safe side.
//   - It prints the real server's lines: the Steam library's start-up
//     chatter, "Game version is …", "REST API started on port …",
//     "Running Palworld dedicated server on :…", and the "[time] [LOG] …"
//     lines for players and REST calls.

func init() {
	register(&Personality{
		ID:      "palworld",
		Version: "v1.0.5.102999",
		MemMB:   48,
		Match:   func(c Call) bool { return c.Program == "palserver-win64-shipping-cmd" },
		Run:     runPalworld,
	})
}

type palPlayer struct {
	user, player string
}

type palworld struct {
	s        *Server
	root     string
	cfgDir   string
	opts     map[string]string // OptionSettings, values unquoted
	id       string
	worldDir string
	port     int
	created  time.Time
	saves    int
	players  map[string]palPlayer
	stopping bool
}

func runPalworld(s *Server) {
	a := s.Args()
	p := &palworld{s: s, port: a.Int("port", 8211), players: map[string]palPlayer{}, created: time.Now()}
	p.root = filepath.Clean(filepath.Join(s.call.Dir, "..", "..", ".."))
	p.cfgDir = filepath.Join(p.root, "Pal", "Saved", "Config", "WindowsServer")
	s.OnCrash(func() {
		s.Log("LowLevelFatalError [File:Unknown] [Line: 0] The stand-in server crashed, as its fakeserver.json asked.")
	})
	s.Log("Setting breakpad minidump AppID = 2394010")
	s.Log("[S_API FAIL] Tried to access Steam interface SteamUser021 before SteamAPI_Init succeeded.")

	p.opts = p.readSettings()
	p.id = p.worldChoice()
	p.worldDir = filepath.Join(p.root, "Pal", "Saved", "SaveGames", "0", p.id)
	if b, err := os.ReadFile(filepath.Join(p.worldDir, "LevelMeta.sav")); err == nil {
		fmt.Sscanf(string(b), "saves=%d", &p.saves)
	}
	s.Sleep(2 * time.Second) // loading the world

	if err := s.ListenUDP(p.port); err != nil {
		s.Logf("Failed to bind UDP port %d: %v", p.port, err)
		s.Exit(1)
	}
	s.Log("Game version is " + s.Version())
	if strings.EqualFold(p.opts["RESTAPIEnabled"], "True") {
		rest, _ := strconv.Atoi(p.opts["RESTAPIPort"])
		if rest == 0 {
			rest = 8212
		}
		if err := s.HTTP(rest, false, p.rest); err != nil {
			s.Logf("Failed to bind REST API port %d: %v", rest, err)
			s.Exit(1)
		}
		s.Logf("REST API started on port %d", rest)
	}
	s.OnStop(func() { s.Exit(0) })
	s.OnStep(p.step)
	span, err := strconv.ParseFloat(p.opts["AutoSaveSpan"], 64)
	if err != nil || span <= 0 {
		span = 30
	}
	s.Every(time.Duration(span*float64(time.Second)), p.save)
	s.Ready(fmt.Sprintf("Running Palworld dedicated server on :%d", p.port))
}

// log writes a "[LOG]" line with the time before it, as the real server does.
func (p *palworld) log(format string, args ...any) {
	p.s.Log("[" + time.Now().Format("2006-01-02 15:04:05") + "] [LOG] " + fmt.Sprintf(format, args...))
}

// readSettings reads PalWorldSettings.ini's OptionSettings. With no file
// (or no options line) the real server writes a file with just its section
// and runs on its own defaults, with the REST API off.
func (p *palworld) readSettings() map[string]string {
	path := filepath.Join(p.cfgDir, "PalWorldSettings.ini")
	b, err := os.ReadFile(path)
	opts := map[string]string{}
	inner := ""
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "OptionSettings="); ok && strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
			inner = v[1 : len(v)-1]
		}
	}
	if err != nil || inner == "" {
		os.MkdirAll(p.cfgDir, 0o755)
		os.WriteFile(path, []byte("[/Script/Pal.PalGameWorldSettings]\r\n"), 0o644)
		return opts
	}
	for _, part := range palSplit(inner) {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			v = strings.TrimSpace(v)
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = v[1 : len(v)-1]
			}
			opts[strings.TrimSpace(k)] = v
		}
	}
	return opts
}

// palSplit splits at commas outside quotes and brackets.
func palSplit(s string) []string {
	var out []string
	depth, start, quoted := 0, 0, false
	for i, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// worldChoice reads DedicatedServerName, or makes a new id and writes it.
func (p *palworld) worldChoice() string {
	path := filepath.Join(p.cfgDir, "GameUserSettings.ini")
	b, _ := os.ReadFile(path)
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "DedicatedServerName="); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	id := make([]byte, 16)
	rand.Read(id)
	name := strings.ToUpper(hex.EncodeToString(id))
	text := string(b)
	switch {
	case strings.Contains(text, "DedicatedServerName="):
		var lines []string
		for line := range strings.Lines(text) {
			if strings.HasPrefix(strings.TrimSpace(line), "DedicatedServerName=") {
				line = "DedicatedServerName=" + name + "\r\n"
			}
			lines = append(lines, line)
		}
		text = strings.Join(lines, "")
	default:
		text = "[/Script/Pal.PalGameLocalSettings]\r\nDedicatedServerName=" + name + "\r\n" + text
	}
	os.MkdirAll(p.cfgDir, 0o755)
	os.WriteFile(path, []byte(text), 0o644)
	return name
}

// ids are a player's platform id and player id, made up from the name the
// way they look on the real server ("steam_" and 17 digits; 8 hex digits
// followed by zeros).
func palIDs(name string) palPlayer {
	h := fnv.New64a()
	h.Write([]byte(name))
	n := h.Sum64()
	return palPlayer{
		user:   fmt.Sprintf("steam_7656119%010d", n%10000000000),
		player: fmt.Sprintf("%08X%024d", uint32(n), 0),
	}
}

func (p *palworld) step(st Step) bool {
	switch st.Verb {
	case "join":
		if len(p.s.Players()) >= p.maxPlayers() {
			return true // the real server turns them away in the game, with no line
		}
		if p.s.Join(st.Name) {
			ids := palIDs(st.Name)
			p.players[st.Name] = ids
			p.log("%s 127.0.0.1 connected the server. (User id: %s)", st.Name, ids.user)
			p.log("%s joined the server. (User id: %s, Player id: %s)", st.Name, ids.user, ids.player)
		}
	case "leave":
		if p.s.Leave(st.Name) {
			p.log("%s left the server. (User id: %s)", st.Name, p.players[st.Name].user)
		}
	case "death", "deny", "count":
		// The real server writes no line for these.
	case "save":
		p.save()
	default:
		return false
	}
	return true
}

func (p *palworld) maxPlayers() int {
	n := p.s.Args().Int("players", 0)
	if n <= 0 {
		n, _ = strconv.Atoi(p.opts["ServerPlayerMaxNum"])
	}
	if n <= 0 {
		n = 32
	}
	return n
}

// save writes the world's files; each save makes Level.sav a little bigger,
// as a played world grows. With bIsUseBackupSaveData (on unless the file
// says False) it also keeps rolling copies in backup/world/, the newest 3.
func (p *palworld) save() {
	p.saves++
	os.MkdirAll(filepath.Join(p.worldDir, "Players"), 0o755)
	os.WriteFile(filepath.Join(p.worldDir, "Level.sav"), bytes.Repeat([]byte{byte(p.saves)}, 2000+100*p.saves), 0o644)
	os.WriteFile(filepath.Join(p.worldDir, "LevelMeta.sav"), fmt.Appendf(nil, "saves=%d\nname=%s\n", p.saves, p.opts["ServerName"]), 0o644)
	for _, name := range p.s.Players() {
		os.WriteFile(filepath.Join(p.worldDir, "Players", p.players[name].player+".sav"), []byte(name), 0o644)
	}
	if !strings.EqualFold(p.opts["bIsUseBackupSaveData"], "False") {
		dir := filepath.Join(p.worldDir, "backup", "world")
		stamp := filepath.Join(dir, fmt.Sprintf("%s.%06d", time.Now().UTC().Format("2006.01.02-15.04.05"), p.saves))
		os.MkdirAll(stamp, 0o755)
		os.WriteFile(filepath.Join(stamp, "Level.sav"), bytes.Repeat([]byte{byte(p.saves)}, 2000+100*p.saves), 0o644)
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 3 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			slices.Sort(names)
			for _, n := range names[:len(names)-3] {
				os.RemoveAll(filepath.Join(dir, n))
			}
		}
	}
}

// rest answers the REST API.
func (p *palworld) rest(w http.ResponseWriter, r *http.Request) {
	if user, pw, ok := r.BasicAuth(); !ok || user != "admin" || pw != p.opts["AdminPassword"] {
		p.log("REST accessed endpoint %s Unauthorized", r.URL.Path)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ok := func() {
		p.log("REST accessed endpoint %s OK", r.URL.Path)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/api/info":
		ok()
		WriteJSON(w, http.StatusOK, map[string]string{"version": p.s.Version(), "servername": p.opts["ServerName"],
			"description": p.opts["ServerDescription"], "worldguid": p.id})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/api/players":
		ok()
		list := []map[string]any{}
		for _, name := range p.s.Players() {
			ids := p.players[name]
			list = append(list, map[string]any{"name": name, "accountName": strings.ToLower(name), "playerId": ids.player,
				"userId": ids.user, "ip": "127.0.0.1", "ping": 12.5, "location_x": 0, "location_y": 0, "level": 1, "building_count": 0})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"players": list})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/api/metrics":
		ok()
		WriteJSON(w, http.StatusOK, map[string]any{"serverfps": 60, "currentplayernum": len(p.s.Players()), "serverframetime": 16.7,
			"maxplayernum": p.maxPlayers(), "uptime": int(time.Since(p.created).Seconds()), "basecampnum": 0, "days": 1})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/api/save":
		ok()
		p.save()
		w.Write([]byte("OK"))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/api/announce":
		var body struct {
			Message *string `json:"message"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Message == nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		ok()
		w.Write([]byte("OK"))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/api/shutdown":
		var body struct {
			WaitTime *int   `json:"waittime"`
			Message  string `json:"message"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.WaitTime == nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		ok()
		w.Write([]byte("OK"))
		if !p.stopping {
			p.stopping = true
			wait := time.Duration(max(*body.WaitTime, 0)) * time.Second
			time.AfterFunc(wait, func() {
				p.s.post(func() {
					p.save()
					p.s.Log("REST API stopped")
					p.s.Exit(0)
				})
			})
		}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/api/stop":
		ok()
		w.Write([]byte("OK"))
		p.s.post(func() { p.s.Exit(0) }) // after the answer has gone out; no save
	default:
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}
