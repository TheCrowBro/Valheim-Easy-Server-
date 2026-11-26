package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The satisfactory personality stands in for the Satisfactory 1.x
// dedicated server, FactoryServer-Win64-Shipping-Cmd(.exe), as far as the
// app can see it (internal/games/satisfactory/FACTS.md has the real one's
// facts):
//
//	FactoryServer-Win64-Shipping-Cmd FactoryGame -Port=7777 -ReliablePort=8888
//	    -ini:Game:[/Script/Engine.GameSession]:MaxPlayers=4 -log -unattended
//
//   - It writes its log itself, in Unreal Engine's format, to
//     <install>/FactoryGame/Saved/Logs/FactoryGame.log (an older one is
//     renamed FactoryGame-backup-<date>.log first), including the API
//     certificate's fingerprint and the join and leave lines.
//   - It listens on the game port (UDP, and TCP for its HTTPS API) and the
//     reliable messaging port (TCP).
//   - Its HTTPS API answers the functions the app uses, with a certificate
//     the stand-in made for itself and keeps, in camelCase and with the
//     real server's error shapes: the server is unclaimed until
//     ClaimServer, needs every newGameData field in CreateNewGame, and is
//     busy (answers nothing) while it loads a world.
//   - It keeps its saves where the real one does, in
//     %LOCALAPPDATA%\FactoryGame\Saved\SaveGames\server (the app points
//     LOCALAPPDATA at the instance's saves folder in fake mode; without it,
//     under its own install folder, never the real user's folder), as .sav
//     files with the real header, and its claim, passwords, certificate and
//     the session to load at start in ServerSettings.<port>.sav beside them
//     (JSON here; the real one is a binary save).
//   - It autosaves every 5 minutes (on its own clock), rotating three
//     <session>_autosave_<n>.sav, and saves on the API's SaveGame; Shutdown
//     exits; Ctrl+C exits without saving (whether the real one saves then
//     is still to be checked, so the stand-in assumes it doesn't).
//   - Scripted players join once a world is loaded; QueryServerState
//     counts them.

func init() {
	register(&Personality{
		ID:      "satisfactory",
		Version: "1.2.0-CL-520140",
		MemMB:   64,
		Match: func(c Call) bool {
			return c.Program == "factoryserver-win64-shipping-cmd" || c.Program == "factoryserver-linux-shipping"
		},
		Run: runSatisfactory,
	})
}

// sfSettings is the stand-in's ServerSettings.<port>.sav.
type sfSettings struct {
	Claimed             bool   `json:"claimed"`
	ServerName          string `json:"serverName"`
	AdminPassword       string `json:"adminPassword"`
	ClientPassword      string `json:"clientPassword"`
	AutoLoadSessionName string `json:"autoLoadSessionName"`
	CertPEM             string `json:"certPEM"`
	KeyPEM              string `json:"keyPEM"`
}

type sfServer struct {
	s            *Server
	root         string // the install folder
	saveGames    string // …/FactoryGame/Saved/SaveGames
	port         int
	reliable     int
	maxPlayers   int
	cl           int
	settingsPath string
	set          sfSettings
	tokens       map[string]string // token → privilege level
	ids          map[string]string // player → account

	session   string
	running   bool
	started   time.Time // when the session was loaded
	played    int       // seconds the session had been played when loaded
	autosave  int
	saves     int
	stopping  bool
	frame     int
	connCount int
}

func runSatisfactory(s *Server) {
	a := s.Args()
	f := &sfServer{s: s, port: a.Int("Port", 7777), reliable: a.Int("ReliablePort", 8888),
		maxPlayers: a.Int("ini:Game:[/Script/Engine.GameSession]:MaxPlayers", 4),
		tokens:     map[string]string{}, ids: map[string]string{}}
	if _, cl, ok := strings.Cut(s.Version(), "-CL-"); ok {
		f.cl, _ = strconv.Atoi(cl)
	}
	// The program is <install>/Engine/Binaries/Win64/<exe>.
	f.root = filepath.Dir(filepath.Dir(filepath.Dir(s.call.Dir)))
	if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
		f.saveGames = filepath.Join(lad, "FactoryGame", "Saved", "SaveGames")
	} else {
		f.saveGames = filepath.Join(f.root, "FactoryGame", "Saved", "SaveGames")
	}
	os.MkdirAll(filepath.Join(f.saveGames, "server"), 0o755)
	f.settingsPath = filepath.Join(f.saveGames, "ServerSettings."+strconv.Itoa(f.port)+".sav")

	logPath := filepath.Join(f.root, "FactoryGame", "Saved", "Logs", "FactoryGame.log")
	if st, err := os.Stat(logPath); err == nil {
		os.Rename(logPath, filepath.Join(filepath.Dir(logPath), "FactoryGame-backup-"+st.ModTime().UTC().Format("2006.01.02-15.04.05")+".log"))
	}
	s.LogTo(logPath)
	s.OnCrash(func() { f.log("LogWindows: Error: === Critical error: ===") })
	s.Log("Log file open, " + time.Now().Format("01/02/06 15:04:05"))
	s.Log("LogInit: Display: Running engine for game: FactoryGame")
	s.Log("LogInit: Build: ++FactoryGame+rel-main-" + s.Version())
	s.Log("LogInit: Net CL: " + strconv.Itoa(f.cl))
	s.Log("LogInit: Command Line: " + strings.Join(a.List()[min(1, len(a.List())):], " "))

	f.loadSettings()
	cert, fresh, err := f.certificate()
	if err != nil {
		f.log("LogServer: Error: Couldn't make the Server API's certificate: " + err.Error())
		s.Exit(1)
	}
	s.Sleep(5 * time.Second) // the engine starting up

	f.listen("game", f.port, s.ListenUDP)
	f.listen("reliable messaging", f.reliable, s.ListenTCP)
	f.log("LogHttpServerModule: Starting all listeners...")
	if fresh {
		f.log("LogServer: Display: Generated a new Self-Signed certificate for the Server API (old certificate invalid or expired)")
	}
	f.log("LogServer: Warning: ==============================================================")
	f.log("LogServer: Warning: Server API is running using a Self-Signed Certificate.")
	f.log("LogServer: Warning: To verify the certificate integrity on the Client, make sure the following Fingerprint matches with the Client one: ")
	f.log("LogServer: Warning: " + sfFingerprint(cert))
	f.log("LogServer: Warning: ==============================================================")
	f.listen("Server API", f.port, func(port int) error { return f.serveAPI(port, cert) })
	f.log(fmt.Sprintf("LogHttpListener: Created new HttpListener on %s", s.addr(f.port)))
	f.log(fmt.Sprintf("LogServer: Display: Server API listening on '%s' (Standalone)", s.addr(f.port)))
	f.log(fmt.Sprintf("LogNet: GameNetDriver IpNetDriver_2147482574 IpNetDriver listening on port %d", f.port))

	s.OnStop(func() {
		f.log("LogCore: Warning: *** INTERRUPTED *** : SHUTTING DOWN")
		f.exit()
	})
	s.OnStep(f.step)
	s.Every(5*time.Minute, f.autosaveNow)

	// As the real server: load the newest save of the session set to load
	// at start, if there is one; otherwise wait for a new game.
	if name := f.set.AutoLoadSessionName; name != "" {
		if save, ok := f.newestSave(name); ok {
			s.post(func() { f.load(save, name) })
		}
	}
}

// log writes one line with the engine's time (UTC) and frame number.
func (f *sfServer) log(msg string) {
	f.frame++
	f.s.Log(fmt.Sprintf("[%s:%03d][%3d]%s", time.Now().UTC().Format("2006.01.02-15.04.05"), time.Now().Nanosecond()/1e6, f.frame%1000, msg))
}

// listen opens a port or stops the server the way it does when another
// program has it.
func (f *sfServer) listen(what string, port int, open func(int) error) {
	if err := open(port); err != nil {
		f.log(fmt.Sprintf("LogNet: Error: Failed to bind the %s port %d: address already in use (%v)", what, port, err))
		f.s.Exit(1)
	}
}

// exit is the server shutting down.
func (f *sfServer) exit() {
	if f.stopping {
		return
	}
	f.stopping = true
	f.log("LogExit: Preparing to exit.")
	f.log("LogExit: Exiting.")
	f.s.Exit(0)
}

// ---- settings and certificate ----

func (f *sfServer) loadSettings() {
	b, err := os.ReadFile(f.settingsPath)
	if err == nil && json.Unmarshal(b, &f.set) == nil {
		return
	}
	f.set = sfSettings{}
}

func (f *sfServer) saveSettings() {
	b, _ := json.MarshalIndent(f.set, "", "  ")
	tmp := f.settingsPath + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, f.settingsPath)
	}
}

// certificate is the API's certificate, kept in the settings as the real
// server keeps its own; fresh says it was made just now.
func (f *sfServer) certificate() (tls.Certificate, bool, error) {
	if f.set.CertPEM != "" {
		if c, err := tls.X509KeyPair([]byte(f.set.CertPEM), []byte(f.set.KeyPEM)); err == nil {
			return c, false, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "FactoryGame Dedicated Server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	f.set.CertPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	f.set.KeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
	f.saveSettings()
	c, err := tls.X509KeyPair([]byte(f.set.CertPEM), []byte(f.set.KeyPEM))
	return c, true, err
}

// sfFingerprint is the certificate's key fingerprint as the log shows it.
func sfFingerprint(c tls.Certificate) string {
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return "SHA256:?"
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:])
}

// ---- the HTTPS API ----

// serveAPI serves the API over TLS on the game port's TCP side. Each call
// runs on the server's goroutine, so a call that arrives while a world is
// loading waits, as with the real server.
func (f *sfServer) serveAPI(port int, cert tls.Certificate) error {
	l, err := net.Listen("tcp", f.s.addr(port))
	if err != nil {
		return err
	}
	tl := tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}})
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				return
			}
			f.s.Do(func() { f.api(w, r, body) })
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	f.s.closeOnExit(srv)
	go srv.Serve(tl)
	return nil
}

// sfError answers an error the way the real server does.
func sfError(w http.ResponseWriter, status int, code, msg string) {
	WriteJSON(w, status, map[string]any{"errorCode": code, "errorMessage": msg, "errorData": map[string]any{}})
}

func sfOK(w http.ResponseWriter, status int, data any) {
	if data == nil {
		w.WriteHeader(status)
		return
	}
	WriteJSON(w, status, map[string]any{"data": data})
}

// sfParams are a call's parameters, by name in lower case: the real server
// reads names without regard to case.
type sfParams map[string]json.RawMessage

func (p sfParams) str(name string) (string, bool) {
	raw, ok := p[strings.ToLower(name)]
	if !ok {
		return "", false
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	return v, true
}

func lowerKeys(raw json.RawMessage) sfParams {
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	out := sfParams{}
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

func missing(w http.ResponseWriter, names ...string) {
	sfError(w, http.StatusOK, "missing_params", "The Request is missing the Required Parameters needed to Execute the Function, or they are in the Invalid Format.\nMissing Parameters:\n"+strings.Join(names, "\n"))
}

// privilege levels, from least to most.
var sfRank = map[string]int{"NotAuthenticated": 0, "Client": 1, "Administrator": 2}

func (f *sfServer) issue(level string) string {
	payload, _ := json.Marshal(map[string]string{"pl": level})
	b := make([]byte, 32)
	rand.Read(b)
	tok := base64.StdEncoding.EncodeToString(payload) + "." + strings.ToUpper(hex.EncodeToString(b))
	f.tokens[tok] = level
	return tok
}

func (f *sfServer) revoke(levels ...string) {
	for tok, l := range f.tokens {
		for _, want := range levels {
			if l == want {
				delete(f.tokens, tok)
			}
		}
	}
}

func (f *sfServer) api(w http.ResponseWriter, r *http.Request, body []byte) {
	if strings.TrimSuffix(r.URL.Path, "/") != "/api/v1" {
		sfError(w, http.StatusNotFound, "errors.com.epicgames.httpserver.route_handler_not_found", "No route handler found")
		return
	}
	var env struct {
		Function string          `json:"function"`
		Data     json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &env) != nil {
		sfError(w, http.StatusBadRequest, "bad_request", "The request body isn't valid JSON")
		return
	}
	p := lowerKeys(env.Data)
	level := ""
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || strings.TrimSpace(tok) == "" {
			sfError(w, http.StatusBadRequest, "bad_request", "Malformed Authorization header")
			return
		}
		if level, ok = f.tokens[tok]; !ok {
			sfError(w, http.StatusUnauthorized, "invalid_token", "The provided authentication token is invalid or has expired")
			return
		}
	}
	need := func(levels ...string) bool {
		for _, l := range levels {
			if level == l {
				return true
			}
		}
		sfError(w, http.StatusForbidden, "insufficient_scope", "The client is missing required privileges to access the given function.")
		return false
	}
	switch strings.ToLower(env.Function) {
	case "healthcheck":
		sfOK(w, http.StatusOK, map[string]string{"health": "healthy", "serverCustomData": ""})
	case "passwordlesslogin":
		min, ok := p.str("minimumPrivilegeLevel")
		if !ok {
			missing(w, "minimumPrivilegeLevel")
			return
		}
		switch {
		case !f.set.Claimed && min != "Administrator":
			sfOK(w, http.StatusOK, map[string]string{"authenticationToken": f.issue("InitialAdmin")})
		case f.set.Claimed && f.set.ClientPassword == "" && sfRank[min] <= sfRank["Client"] && min != "InitialAdmin":
			sfOK(w, http.StatusOK, map[string]string{"authenticationToken": f.issue("Client")})
		default:
			sfError(w, http.StatusOK, "passwordless_login_not_possible", "Passwordless login is not possible at this time")
		}
	case "passwordlogin":
		min, ok1 := p.str("minimumPrivilegeLevel")
		pw, ok2 := p.str("password")
		if !ok1 || !ok2 {
			missing(w, "minimumPrivilegeLevel", "password")
			return
		}
		switch {
		case f.set.Claimed && pw == f.set.AdminPassword && min != "InitialAdmin":
			sfOK(w, http.StatusOK, map[string]string{"authenticationToken": f.issue("Administrator")})
		case f.set.Claimed && f.set.ClientPassword != "" && pw == f.set.ClientPassword && sfRank[min] <= sfRank["Client"]:
			sfOK(w, http.StatusOK, map[string]string{"authenticationToken": f.issue("Client")})
		default:
			sfError(w, http.StatusOK, "wrong_password", "Wrong password")
		}
	case "claimserver":
		if !need("InitialAdmin") {
			return
		}
		name, ok1 := p.str("serverName")
		admin, ok2 := p.str("adminPassword")
		if !ok1 || !ok2 {
			missing(w, "serverName", "adminPassword")
			return
		}
		if f.set.Claimed {
			sfError(w, http.StatusOK, "server_claimed", "The server has already been claimed")
			return
		}
		f.set.Claimed, f.set.ServerName, f.set.AdminPassword = true, name, admin
		f.saveSettings()
		f.revoke("InitialAdmin")
		f.log(fmt.Sprintf("LogServer: Display: Server claimed as '%s'", name))
		sfOK(w, http.StatusOK, map[string]string{"authenticationToken": f.issue("Administrator")})
	case "renameserver", "setclientpassword", "setadminpassword":
		f.serverSettings(w, strings.ToLower(env.Function), p, need)
	case "queryserverstate":
		if !need("Administrator", "Client", "APIToken") {
			return
		}
		sfOK(w, http.StatusOK, map[string]any{"serverGameState": f.gameState()})
	case "createnewgame":
		if !need("Administrator", "APIToken") {
			return
		}
		f.createNewGame(w, p)
	case "loadgame":
		if !need("Administrator", "APIToken") {
			return
		}
		save, ok := p.str("saveName")
		if !ok {
			missing(w, "saveName")
			return
		}
		h, err := sfReadHeader(filepath.Join(f.saveGames, "server", save+".sav"))
		if err != nil {
			sfError(w, http.StatusOK, "save_game_load_failed", "Failed to find the save game file '"+save+"'")
			return
		}
		sfOK(w, http.StatusAccepted, nil)
		f.s.post(func() { f.load(save, h.session) })
	case "savegame":
		if !need("Administrator", "APIToken") {
			return
		}
		save, ok := p.str("saveName")
		if !ok {
			missing(w, "saveName")
			return
		}
		if !f.running {
			sfError(w, http.StatusOK, "save_game_failed", "No game is running")
			return
		}
		if err := f.writeSave(save); err != nil {
			sfError(w, http.StatusOK, "save_game_failed", err.Error())
			return
		}
		sfOK(w, http.StatusNoContent, nil)
	case "setautoloadsessionname":
		if !need("Administrator", "APIToken") {
			return
		}
		name, ok := p.str("sessionName")
		if !ok {
			missing(w, "sessionName")
			return
		}
		f.set.AutoLoadSessionName = name
		f.saveSettings()
		sfOK(w, http.StatusNoContent, nil)
	case "enumeratesessions":
		if !need("Administrator", "APIToken") {
			return
		}
		sfOK(w, http.StatusOK, f.sessions())
	case "shutdown":
		if !need("Administrator", "APIToken") {
			return
		}
		f.log("LogServer: Warning: Received a Shutdown request through the Server API from 127.0.0.1")
		sfOK(w, http.StatusNoContent, nil)
		f.s.post(f.exit) // after the answer has gone out
	default:
		sfError(w, http.StatusNotFound, "function_not_found", "The function '"+env.Function+"' doesn't exist")
	}
}

// serverSettings answers RenameServer, SetClientPassword and SetAdminPassword.
func (f *sfServer) serverSettings(w http.ResponseWriter, fn string, p sfParams, need func(...string) bool) {
	if !need("Administrator", "APIToken") {
		return
	}
	if !f.set.Claimed {
		sfError(w, http.StatusOK, "server_not_claimed", "The server has not been claimed yet")
		return
	}
	switch fn {
	case "renameserver":
		name, ok := p.str("serverName")
		if !ok {
			missing(w, "serverName")
			return
		}
		f.set.ServerName = name
		f.saveSettings()
		sfOK(w, http.StatusNoContent, nil)
	case "setclientpassword":
		pw, ok := p.str("password")
		if !ok {
			missing(w, "password")
			return
		}
		if pw != "" && pw == f.set.AdminPassword {
			sfError(w, http.StatusOK, "password_in_use", "The password is already used as the admin password")
			return
		}
		f.set.ClientPassword = pw
		f.saveSettings()
		f.revoke("Client")
		sfOK(w, http.StatusNoContent, nil)
	case "setadminpassword":
		pw, ok := p.str("password")
		if !ok {
			missing(w, "password")
			return
		}
		switch {
		case pw == "":
			sfError(w, http.StatusOK, "cannot_reset_admin_password", "The admin password can't be removed")
		case pw == f.set.ClientPassword:
			sfError(w, http.StatusOK, "password_in_use", "The password is already used as the client password")
		default:
			f.set.AdminPassword = pw
			f.saveSettings()
			f.revoke("Client", "Administrator")
			sfOK(w, http.StatusOK, map[string]string{"authenticationToken": f.issue("Administrator")})
		}
	}
}

// newGameFields are the fields CreateNewGame's newGameData must have, as
// the real server names them.
var newGameFields = []string{"SessionName", "MapName", "StartingLocation", "bSkipOnboarding",
	"AdvancedGameSettings", "GameModeSettings", "CustomOptionsOnlyForModding"}

func (f *sfServer) createNewGame(w http.ResponseWriter, p sfParams) {
	raw, ok := p["newgamedata"]
	if !ok {
		missing(w, "newGameData")
		return
	}
	data := lowerKeys(raw)
	for _, name := range newGameFields {
		if _, ok := data[strings.ToLower(name)]; !ok {
			msg := "Unable to import JSON object into FGServerNewGameData property NewGameData\nMissing JSON value named " + name
			WriteJSON(w, http.StatusOK, map[string]any{"errorCode": "missing_params",
				"errorMessage": "The Request is missing the Required Parameters needed to Execute the Function, or they are in the Invalid Format.\nInvalid Parameters:\nnewGameData: " + msg,
				"errorData":    map[string]any{"missingParameters": []string{}, "invalidParameters": map[string]string{"newGameData": msg}}})
			return
		}
	}
	session, _ := data.str("sessionName")
	if strings.TrimSpace(session) == "" {
		sfError(w, http.StatusOK, "invalid_params", "The session name can't be empty")
		return
	}
	start, _ := data.str("startingLocation")
	sfOK(w, http.StatusAccepted, nil)
	f.s.post(func() {
		f.log(fmt.Sprintf("LogGame: Display: Creating a new game '%s' (starting location '%s')", session, start))
		f.load("", session)
	})
}

// load loads a save ("" for a new game) of session. The server is busy the
// whole time: nothing else runs, API calls included.
func (f *sfServer) load(save, session string) {
	for _, p := range f.s.Players() {
		f.s.Leave(p)
		f.closeLine(p)
	}
	f.running = false
	f.log("LogLoad: LoadMap: /Game/FactoryGame/Map/GameLevel01/Persistent_Level?sessionName=" + session)
	f.s.Sleep(20 * time.Second)
	f.played = 0
	if save != "" {
		if h, err := sfReadHeader(filepath.Join(f.saveGames, "server", save+".sav")); err == nil {
			f.played = int(h.played)
		}
	}
	f.session, f.running, f.started = session, true, time.Now()
	f.log("LogWorld: Bringing World /Game/FactoryGame/Map/GameLevel01/Persistent_Level.Persistent_Level up for play (max tick rate 30) at " + time.Now().UTC().Format("2006.01.02-15.04.05"))
	f.s.Ready(fmt.Sprintf("[%s:000][  0]LogLoad: Took 20.0 seconds to LoadMap(/Game/FactoryGame/Map/GameLevel01/Persistent_Level)", time.Now().UTC().Format("2006.01.02-15.04.05")))
}

func (f *sfServer) gameState() map[string]any {
	running := f.running && !f.s.Config().NoReady
	dur := f.played
	if f.running {
		dur += int(f.s.Scaled(time.Since(f.started)) / time.Second)
	}
	phase := "None"
	if running {
		phase = "/Script/FactoryGame.FGGamePhase'/Game/FactoryGame/GamePhases/GP_Project_Assembly_Phase_0.GP_Project_Assembly_Phase_0'"
	}
	return map[string]any{
		"activeSessionName": f.session, "numConnectedPlayers": len(f.s.Players()), "playerLimit": f.maxPlayers,
		"techTier": 0, "activeSchematic": "None", "gamePhase": phase, "isGameRunning": running,
		"totalGameDuration": dur, "isGamePaused": running && len(f.s.Players()) == 0, "averageTickRate": 29.9,
		"autoLoadSessionName": f.set.AutoLoadSessionName, "agreeToCrashUploadRequested": false,
	}
}

// ---- saves ----

func (f *sfServer) autosaveNow() {
	if !f.running {
		return
	}
	name := fmt.Sprintf("%s_autosave_%d", f.session, f.autosave)
	f.autosave = (f.autosave + 1) % 3
	f.writeSave(name)
}

// writeSave writes <save>.sav with the real header and a body that grows
// a little with every save, as a played world does.
func (f *sfServer) writeSave(save string) error {
	if strings.ContainsAny(save, `\/:*?"<>|`) || strings.TrimSpace(save) == "" {
		return fmt.Errorf("the save name %q can't be a file name", save)
	}
	f.saves++
	played := f.played + int(f.s.Scaled(time.Since(f.started))/time.Second)
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	fstr := func(s string) {
		if s == "" {
			le(int32(0))
			return
		}
		le(int32(len(s) + 1))
		b.WriteString(s)
		b.WriteByte(0)
	}
	le(int32(14))   // header version
	le(int32(52))   // save version
	le(int32(f.cl)) // build version
	fstr(save)      // save name
	fstr("Persistent_Level")
	fstr("?sessionName=" + f.session + "?Visibility=SV_FriendsOnly")
	fstr(f.session)
	le(int32(played))
	le(sfTicks(time.Now()))
	b.WriteByte(0) // session visibility
	le(int32(40))  // editor object version
	fstr("")       // mod metadata
	le(int32(0))   // mod flags
	fstr(fmt.Sprintf("%X", md5.Sum([]byte(save+time.Now().String()))))
	le(int32(1)) // partitioned world
	body := bytes.Repeat([]byte{byte(f.saves)}, 4096+128*f.saves+64*len(f.s.Players()))
	sum := md5.Sum(body)
	le(int32(1))
	b.Write(sum[:])
	le(int32(0)) // creative mode
	b.Write(body)

	dir := filepath.Join(f.saveGames, "server")
	os.MkdirAll(dir, 0o755)
	f.log(fmt.Sprintf("LogGame: Display: Saving game '%s' (session '%s')", save, f.session))
	tmp := filepath.Join(dir, save+".sav.tmp")
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, save+".sav")); err != nil {
		return err
	}
	f.log("LogGame: World Serialization (save): 0.12 seconds")
	return nil
}

// sfTicks is a time in .NET ticks, as the save header keeps it.
func sfTicks(t time.Time) int64 {
	return 621355968000000000 + t.UnixNano()/100
}

type sfHeader struct {
	session string
	played  int32
	saved   int64
}

// sfReadHeader reads the session out of a save the stand-in wrote.
func sfReadHeader(path string) (sfHeader, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return sfHeader{}, err
	}
	r := bytes.NewReader(b)
	var v int32
	rd := func(x any) { binary.Read(r, binary.LittleEndian, x) }
	fstr := func() string {
		var n int32
		rd(&n)
		if n <= 0 || int(n) > r.Len() {
			return ""
		}
		s := make([]byte, n)
		r.Read(s)
		return strings.TrimRight(string(s), "\x00")
	}
	rd(&v)
	if v != 14 {
		return sfHeader{}, errors.New("not a save the stand-in knows")
	}
	rd(&v)
	rd(&v)
	fstr()
	fstr()
	fstr()
	h := sfHeader{session: fstr()}
	rd(&h.played)
	rd(&h.saved)
	return h, nil
}

// newestSave finds session's newest save.
func (f *sfServer) newestSave(session string) (string, bool) {
	best, bestAt := "", int64(-1)
	for name, h := range f.saveList() {
		if strings.EqualFold(h.session, session) && h.saved > bestAt {
			best, bestAt = name, h.saved
		}
	}
	return best, best != ""
}

func (f *sfServer) saveList() map[string]sfHeader {
	out := map[string]sfHeader{}
	entries, _ := os.ReadDir(filepath.Join(f.saveGames, "server"))
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".sav"); ok {
			if h, err := sfReadHeader(filepath.Join(f.saveGames, "server", e.Name())); err == nil {
				out[name] = h
			}
		}
	}
	return out
}

// sessions is EnumerateSessions' answer.
func (f *sfServer) sessions() map[string]any {
	by := map[string][]map[string]any{}
	for name, h := range f.saveList() {
		by[h.session] = append(by[h.session], map[string]any{
			"saveVersion": 52, "buildVersion": f.cl, "saveName": name, "saveLocationInfo": "", "mapName": "Persistent_Level",
			"mapOptions": "", "sessionName": h.session, "playDurationSeconds": h.played,
			"saveDateTime": time.Unix(0, (h.saved-621355968000000000)*100).UTC().Format("2006.01.02-15.04.05"),
			"isModdedSave": false, "isEditedSave": false, "isCreativeModeEnabled": false,
		})
	}
	names := make([]string, 0, len(by))
	for n := range by {
		names = append(names, n)
	}
	sort.Strings(names)
	list, current := []map[string]any{}, -1
	for i, n := range names {
		list = append(list, map[string]any{"sessionName": n, "saveHeaders": by[n]})
		if n == f.session {
			current = i
		}
	}
	return map[string]any{"sessions": list, "currentSessionIndex": current}
}

// ---- players ----

func (f *sfServer) account(name string) string {
	if id, ok := f.ids[name]; ok {
		return id
	}
	id := fmt.Sprintf("Steam:765611980000%05d", len(f.ids)+1)
	f.ids[name] = id
	return id
}

func (f *sfServer) closeLine(name string) {
	f.connCount++
	f.log(fmt.Sprintf("LogNet: UNetConnection::Close: [UNetConnection] RemoteAddr: 127.0.0.1:%d, Name: IpConnection_2147481%03d, Driver: GameNetDriver IpNetDriver_2147482574, IsServer: YES, PC: BP_PlayerController_C_2147480%03d, Owner: BP_PlayerController_C_2147480%03d, UniqueId: %s, Channels: 73, Time: %s",
		52000+f.connCount, f.connCount, f.connCount, f.connCount, f.account(name), time.Now().UTC().Format("2006.01.02-15.04.05")))
}

func (f *sfServer) step(st Step) bool {
	switch st.Verb {
	case "join":
		if !f.running {
			f.s.Warnf("%s can't join: no world is loaded", st.Name)
			return true
		}
		f.log(fmt.Sprintf("LogNet: Login request: ?EncryptionToken=0123456789ABCDEF?Name=%s userId: %s platform: Steam", st.Name, f.account(st.Name)))
		if len(f.s.Players()) >= f.maxPlayers {
			f.log(fmt.Sprintf("LogNet: Warning: Login of %s refused: the server is full", st.Name))
			return true
		}
		f.log(fmt.Sprintf("LogNet: Join request: /Game/FactoryGame/Map/GameLevel01/Persistent_Level?Name=%s?SplitscreenCount=1", st.Name))
		if f.s.Join(st.Name) {
			f.log("LogNet: Join succeeded: " + st.Name)
		}
	case "leave":
		if f.s.Leave(st.Name) {
			f.closeLine(st.Name)
		}
	case "deny":
		f.log(fmt.Sprintf("LogNet: Login request: ?EncryptionToken=0123456789ABCDEF?Name=%s userId: %s platform: Steam", st.Name, f.account(st.Name)))
		f.log(fmt.Sprintf("LogNet: Warning: Login of %s refused: wrong password", st.Name))
	case "save":
		f.autosaveNow()
	case "count":
	default:
		return false
	}
	return true
}
