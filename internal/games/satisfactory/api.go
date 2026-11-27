package satisfactory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// The server's HTTPS API (FACTS.md): every call is a POST of
// {"function": <name>, "data": {...}} to https://<host>:<game port>/api/v1.
// An answer is {"data": {...}} or, when the call failed,
// {"errorCode": ..., "errorMessage": ..., "errorData": {...}} (often with
// HTTP status 200). Most functions need a "Bearer" token, which the login
// functions hand out. The server reads parameter names without regard to
// letter case and answers in camelCase, so the app sends camelCase too.
//
// The app only ever talks to the server on this computer, 127.0.0.1.

// Privilege levels the login functions are asked for.
const (
	levelInitialAdmin = "InitialAdmin"
	levelAdmin        = "Administrator"
)

// Error codes the app acts on (FACTS.md).
const (
	codePasswordless = "passwordless_login_not_possible"
	codeWrongPass    = "wrong_password"
	codeClaimed      = "server_claimed"
	codeNotClaimed   = "server_not_claimed"
	codeInvalidToken = "invalid_token"
)

// apiError is a failure the API reported.
type apiError struct {
	Function string
	Status   int    // the HTTP status
	Code     string // errorCode; "" when the answer had none
	Message  string // errorMessage
}

func (e *apiError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	switch {
	case e.Code != "" && msg != "":
		return fmt.Sprintf("Satisfactory's server API turned down %s: %s (%s)", e.Function, msg, e.Code)
	case e.Code != "":
		return fmt.Sprintf("Satisfactory's server API turned down %s (%s)", e.Function, e.Code)
	}
	return fmt.Sprintf("Satisfactory's server API answered %s with HTTP status %d", e.Function, e.Status)
}

// codeOf is the API's error code in err, or "".
func codeOf(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// authFailed says the token was refused: it expired, or a new admin
// password made it invalid. Logging in again fixes it.
func authFailed(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Status == http.StatusUnauthorized || ae.Code == codeInvalidToken ||
		(ae.Status == http.StatusForbidden && ae.Code == "")
}

// pinError: the server's certificate key isn't the one the app knows.
type pinError struct {
	key, cert string // the fingerprints of what it presented
}

func (e *pinError) Error() string {
	return "the program on Satisfactory's port presented a security certificate the app doesn't know (" + e.key + ")"
}

// keyFingerprint is how the app names a certificate's key: SHA-256 of its
// public key, written as the server writes the fingerprint in its log
// ("SHA256:" and base64, FACTS.md).
func keyFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:])
}

// certFingerprint is the same over the whole certificate. The server's log
// may mean either; the app checks the log against both.
func certFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:])
}

// apiClient talks to one running server.
type apiClient struct {
	port  int
	token string
	// greeted: the first contact's HealthCheck already answered, so a
	// caller waiting for the API needn't ask again.
	greeted bool

	mu       sync.Mutex
	pin      string // the key fingerprint to accept; "" accepts any (first contact, before anything secret is sent)
	seenKey  string // what the last connection presented
	seenCert string

	http *http.Client
}

// newAPI makes a client for the server on port that accepts only the
// certificate key pin (any, when pin is "").
//
// The server's certificate is one it made for itself, so no authority can
// vouch for it, and the normal checks would always refuse it. Instead the
// client checks the key itself on every connection (VerifyConnection runs
// before anything is sent), so an admin password only ever goes to the key
// the app trusts. Each call has a connection of its own: one kept from an
// earlier run of the server could outlive it, and a new connection is
// checked again.
func newAPI(port int, pin string) *apiClient {
	c := &apiClient{port: port, pin: pin}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The key is checked in VerifyConnection instead (see above).
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the server presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			key, cert := keyFingerprint(leaf), certFingerprint(leaf)
			c.mu.Lock()
			c.seenKey, c.seenCert = key, cert
			pin := c.pin
			c.mu.Unlock()
			if pin != "" && key != pin {
				return &pinError{key: key, cert: cert}
			}
			return nil
		},
	}
	c.http = &http.Client{
		Transport: &http.Transport{
			Proxy:               nil, // never through a proxy the system may set: this is 127.0.0.1
			DialContext:         (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			TLSClientConfig:     cfg,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   true,
		},
		// Each call also has its context's deadline; this is the most
		// any one may take when the caller gave none.
		Timeout: 2 * time.Minute,
	}
	return c
}

// setPin makes the client accept only key from now on.
func (c *apiClient) setPin(key string) {
	c.mu.Lock()
	c.pin = key
	c.mu.Unlock()
}

// seen is the key and certificate the last connection presented.
func (c *apiClient) seen() (key, cert string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seenKey, c.seenCert
}

// envelope is a request.
type envelope struct {
	Function string `json:"function"`
	Data     any    `json:"data,omitempty"`
}

// answer is a response, success or failure.
type answer struct {
	Data         json.RawMessage `json:"data"`
	ErrorCode    string          `json:"errorCode"`
	ErrorMessage string          `json:"errorMessage"`
}

// maxAnswer caps what the app reads from one answer.
const maxAnswer = 4 << 20

// call runs one function. out, when not nil, receives the answer's data,
// which must then be there.
func (c *apiClient) call(ctx context.Context, function string, data, out any) error {
	raw, err := c.do(ctx, function, data)
	if err != nil || out == nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return fmt.Errorf("Satisfactory's server API answered %s without the data the app needs", function)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Satisfactory's server API gave an answer to %s the app can't read: %w", function, err)
	}
	return nil
}

// do runs one function and returns the answer's data, if it had any.
func (c *apiClient) do(ctx context.Context, function string, data any) (json.RawMessage, error) {
	body, err := json.Marshal(envelope{Function: function, Data: data})
	if err != nil {
		return nil, err
	}
	url := "https://127.0.0.1:" + strconv.Itoa(c.port) + "/api/v1"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		// Never an empty "Bearer": the server refuses that as malformed.
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, err
	}
	var a answer
	if len(bytes.TrimSpace(b)) > 0 {
		if jerr := json.Unmarshal(b, &a); jerr != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil, fmt.Errorf("Satisfactory's server API gave an answer to %s the app can't read: %w", function, jerr)
		}
	}
	if a.ErrorCode != "" || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &apiError{Function: function, Status: resp.StatusCode, Code: a.ErrorCode, Message: a.ErrorMessage}
	}
	return a.Data, nil
}

// The functions the app uses, with camelCase parameters (FACTS.md).

func (c *apiClient) healthCheck(ctx context.Context) (string, error) {
	var out struct {
		Health string `json:"health"`
	}
	err := c.call(ctx, "HealthCheck", map[string]string{"clientCustomData": ""}, &out)
	return out.Health, err
}

type tokenData struct {
	AuthenticationToken string `json:"authenticationToken"`
}

func (c *apiClient) passwordlessLogin(ctx context.Context, level string) (string, error) {
	var out tokenData
	err := c.call(ctx, "PasswordlessLogin", map[string]string{"minimumPrivilegeLevel": level}, &out)
	return out.AuthenticationToken, err
}

func (c *apiClient) passwordLogin(ctx context.Context, level, password string) (string, error) {
	var out tokenData
	err := c.call(ctx, "PasswordLogin", map[string]string{"minimumPrivilegeLevel": level, "password": password}, &out)
	return out.AuthenticationToken, err
}

func (c *apiClient) claimServer(ctx context.Context, name, admin string) (string, error) {
	var out tokenData
	err := c.call(ctx, "ClaimServer", map[string]string{"serverName": name, "adminPassword": admin}, &out)
	return out.AuthenticationToken, err
}

func (c *apiClient) renameServer(ctx context.Context, name string) error {
	return c.call(ctx, "RenameServer", map[string]string{"serverName": name}, nil)
}

// setClientPassword sets the friends' password; "" removes it.
func (c *apiClient) setClientPassword(ctx context.Context, password string) error {
	return c.call(ctx, "SetClientPassword", map[string]string{"password": password}, nil)
}

// setAdminPassword changes the admin password. Every token handed out
// before stops working; the answer may carry a new one ("" when it
// doesn't, and the caller logs in again).
func (c *apiClient) setAdminPassword(ctx context.Context, password string) (string, error) {
	raw, err := c.do(ctx, "SetAdminPassword", map[string]string{"password": password})
	if err != nil {
		return "", err
	}
	var out tokenData
	if len(raw) > 0 {
		json.Unmarshal(raw, &out)
	}
	return out.AuthenticationToken, nil
}

// serverGameState is QueryServerState's answer (a real one is in
// testdata/api).
type serverGameState struct {
	ActiveSessionName   string  `json:"activeSessionName"`
	NumConnectedPlayers int     `json:"numConnectedPlayers"`
	PlayerLimit         int     `json:"playerLimit"`
	TechTier            int     `json:"techTier"`
	ActiveSchematic     string  `json:"activeSchematic"`
	GamePhase           string  `json:"gamePhase"`
	IsGameRunning       bool    `json:"isGameRunning"`
	TotalGameDuration   int     `json:"totalGameDuration"`
	IsGamePaused        bool    `json:"isGamePaused"`
	AverageTickRate     float64 `json:"averageTickRate"`
	AutoLoadSessionName string  `json:"autoLoadSessionName"`
}

func (c *apiClient) queryState(ctx context.Context) (serverGameState, error) {
	var out struct {
		State serverGameState `json:"serverGameState"`
	}
	err := c.call(ctx, "QueryServerState", nil, &out)
	return out.State, err
}

// newGameData is CreateNewGame's newGameData. The server refuses the call
// unless every one of these is there, even the empty ones, and it wants
// the C++ name bSkipOnboarding (FACTS.md, from a real server's answers).
type newGameData struct {
	SessionName                 string            `json:"sessionName"`
	MapName                     string            `json:"mapName"`
	StartingLocation            string            `json:"startingLocation"`
	SkipOnboarding              bool              `json:"bSkipOnboarding"`
	AdvancedGameSettings        map[string]string `json:"advancedGameSettings"`
	GameModeSettings            map[string]string `json:"gameModeSettings"`
	CustomOptionsOnlyForModding map[string]string `json:"customOptionsOnlyForModding"`
}

// createNewGame makes a world and loads it; "" as start lets the game pick
// where the players start. The server is busy loading for a while after.
func (c *apiClient) createNewGame(ctx context.Context, session, start string) error {
	return c.call(ctx, "CreateNewGame", map[string]any{"newGameData": newGameData{
		SessionName: session, StartingLocation: start, SkipOnboarding: true,
		AdvancedGameSettings: map[string]string{}, GameModeSettings: map[string]string{},
		CustomOptionsOnlyForModding: map[string]string{},
	}}, nil)
}

// loadGame loads a save by its file name without ".sav".
func (c *apiClient) loadGame(ctx context.Context, saveName string) error {
	return c.call(ctx, "LoadGame", map[string]any{"saveName": saveName, "enableAdvancedGameSettings": false}, nil)
}

func (c *apiClient) saveGame(ctx context.Context, saveName string) error {
	return c.call(ctx, "SaveGame", map[string]string{"saveName": saveName}, nil)
}

// setAutoLoadSession makes the server load the session's newest save when
// it starts.
func (c *apiClient) setAutoLoadSession(ctx context.Context, session string) error {
	return c.call(ctx, "SetAutoLoadSessionName", map[string]string{"sessionName": session}, nil)
}

func (c *apiClient) shutdown(ctx context.Context) error {
	return c.call(ctx, "Shutdown", nil, nil)
}

// ---- the instance's connection ----

// The instance's State keys (the adapter's own bookkeeping in servers.json,
// spec 2.2). The server keeps its claim, passwords and certificate per game
// port (in ServerSettings.<port>.sav), so the claim and the pinned key
// remember the port they belong to.
const (
	stateClaimed   = "claimed"   // when the app claimed the server (RFC 3339)
	stateClaimPort = "claimPort" // the game port it claimed it on
	stateAdmin     = "admin"     // the admin password the server has now
	stateCert      = "cert"      // the API certificate's key fingerprint the app trusts
	stateCertPort  = "certPort"  // the game port that key belongs to
	stateName      = "name"      // the server name last applied, on the claim's port
	statePassword  = "password"  // a hash of the friends' password last applied, likewise
)

// gamePort is the run's game port.
func (s *server) gamePort(rec game.Record) int {
	if p := rec.Ports["game"]; p > 0 {
		return p
	}
	if rec.Port > 0 {
		return rec.Port
	}
	p, _ := ports(s.store.Get())
	return p
}

// adminFor is the admin password to log in with: the one the server has
// now, as the settings say, else the one the run started with.
func (s *server) adminFor(rec game.Record) string {
	if a := s.store.Get().State[stateAdmin]; a != "" {
		return a
	}
	return rec.Secrets[secretAdmin]
}

// client makes an API client for the run whose certificate is checked: the
// key the app pinned for this port, or, the first time, a key the app has
// checked belongs to the server it started (trustKey). Nothing secret is
// sent before that.
func (s *server) client(ctx context.Context, rec game.Record) (*apiClient, error) {
	port := s.gamePort(rec)
	st := s.store.Get().State
	pin := ""
	if st[stateCertPort] == itoa(port) {
		pin = st[stateCert]
	}
	c := newAPI(port, pin)
	if pin != "" {
		return c, nil
	}
	// First contact: a call that sends nothing secret shows the key.
	if _, err := c.healthCheck(ctx); err != nil {
		var pe *pinError
		if !errors.As(err, &pe) {
			return nil, err
		}
	} else {
		c.greeted = true
	}
	key, cert := c.seen()
	if key == "" {
		return nil, errors.New("Satisfactory's server API didn't present a certificate")
	}
	if err := s.trustKey(rec, port, key, cert, true); err != nil {
		return nil, err
	}
	c.setPin(key)
	return c, nil
}

// checked runs f, and when the server's key has changed since the app
// pinned it, checks the new key (trustKey) and runs f again with it.
func (s *server) checked(rec game.Record, c *apiClient, f func() error) error {
	err := f()
	var pe *pinError
	if !errors.As(err, &pe) {
		return err
	}
	if terr := s.trustKey(rec, c.port, pe.key, pe.cert, false); terr != nil {
		return terr
	}
	c.setPin(pe.key)
	return f()
}

// trustKey decides whether a certificate key the app doesn't know yet is
// the server's, and pins it if so.
//
// The surest sign is that the program holding the API's port is the very
// server process the app started. Where the system can't say who holds
// the port, the server's own log is asked: it writes the fingerprint of
// the certificate it made (FACTS.md). With neither, a key seen at the
// first contact is trusted (nothing else was known), but a changed key is
// not.
func (s *server) trustKey(rec game.Record, port int, key, cert string, first bool) error {
	ok := false
	switch _, pid := portOwner("tcp", port); {
	case pid != 0:
		ok = pid == rec.PID
	default:
		if logged := loggedFingerprint(rec.LogPath); logged != "" {
			ok = logged == key || logged == cert
		} else {
			ok = first
		}
	}
	if !ok {
		return &distrustError{port: port}
	}
	if !first {
		s.log.Printf("the server's API has a new certificate (%s); it is the server the app started, so the app trusts it now", key)
	}
	return s.store.Save(func(sv *game.Saved) error {
		if sv.State == nil {
			sv.State = game.Values{}
		}
		sv.State[stateCert], sv.State[stateCertPort] = key, itoa(port)
		return nil
	})
}

// portOwner says which process holds a port (0 when the system can't
// tell). Tests replace it.
var portOwner = platform.PortInUse

// distrustError: the key belongs to some other program, not the server.
type distrustError struct{ port int }

func (e *distrustError) Error() string {
	return fmt.Sprintf("The program answering on Satisfactory's port %d isn't the Satisfactory server the app started (its security certificate isn't the one the app knows), so the app won't send it the admin password. Close whatever else is using port %d, or choose new ports for Satisfactory.", e.port, e.port)
}

// loggedFingerprint is the certificate fingerprint the server wrote in this
// run's log ("" when it wrote none).
func loggedFingerprint(path string) string {
	if path == "" {
		return ""
	}
	f, err := platform.OpenShared(path)
	if err != nil {
		if f, err = os.Open(path); err != nil {
			return ""
		}
	}
	defer f.Close()
	found := ""
	sc := bufio.NewScanner(io.LimitReader(f, 64<<20))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if m := reFingerprint.FindStringSubmatch(sc.Text()); m != nil {
			found = m[1] // the newest one counts
		}
	}
	return found
}

// admin is an API client for the run, logged in as the administrator,
// reusing the token from the last call while it works.
func (s *server) admin(ctx context.Context, rec game.Record) (*apiClient, error) {
	c, err := s.client(ctx, rec)
	if err != nil {
		return nil, err
	}
	password := s.adminFor(rec)
	if password == "" {
		return nil, errors.New("the app doesn't know this Satisfactory server's admin password yet (it sets it up right after the server's first start)")
	}
	key := itoa(c.port) + "\x00" + password
	s.tokenMu.Lock()
	if s.tokenFor == key {
		c.token = s.token
	}
	s.tokenMu.Unlock()
	if c.token != "" {
		return c, nil
	}
	var tok string
	err = s.checked(rec, c, func() (e error) {
		tok, e = c.passwordLogin(ctx, levelAdmin, password)
		return e
	})
	if err != nil {
		if codeOf(err) == codeWrongPass {
			return nil, errors.New("Satisfactory's server turned down the admin password the app has for it. If you changed it in the game's Server Manager, type the new one under Admin password in Satisfactory's settings.")
		}
		return nil, err
	}
	c.token = tok
	s.keepToken(key, tok)
	return c, nil
}

// keepToken remembers a token for the next call.
func (s *server) keepToken(key, tok string) {
	s.tokenMu.Lock()
	s.token, s.tokenFor = tok, key
	s.tokenMu.Unlock()
}

// dropToken forgets the token, so the next call logs in again.
func (s *server) dropToken() {
	s.tokenMu.Lock()
	s.token, s.tokenFor = "", ""
	s.tokenMu.Unlock()
}

// asAdmin runs f logged in as the administrator, logging in again once if
// the server turned the kept token down.
func (s *server) asAdmin(ctx context.Context, rec game.Record, f func(c *apiClient) error) error {
	c, err := s.admin(ctx, rec)
	if err != nil {
		return err
	}
	err = s.checked(rec, c, func() error { return f(c) })
	if !authFailed(err) {
		return err
	}
	s.dropToken()
	if c, err = s.admin(ctx, rec); err != nil {
		return err
	}
	return s.checked(rec, c, func() error { return f(c) })
}

// refused says the connection was turned down: nothing listens there.
func refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "refused")
}
