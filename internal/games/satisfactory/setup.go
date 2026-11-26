package satisfactory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
)

// The setup is what a player would otherwise do by hand in the game's
// Server Manager, done through the server's API right after each start:
//
//  1. wait for the API to answer, and check its certificate (api.go);
//  2. log in as the administrator, claiming the server the first time
//     (PasswordlessLogin as InitialAdmin, then ClaimServer with the server
//     name and an admin password the app makes and keeps);
//  3. apply the admin password the owner typed, the server name and the
//     friends' password, when they changed;
//  4. load the selected world: nothing to do when the server already
//     loaded it by itself, else LoadGame on its newest save, or
//     CreateNewGame for a new world (saved at once, so it is on disk);
//     then make it the session the server loads when it starts.
//
// It runs after each launch from Afterer.After, which holds the "online"
// post until it is done. The probe also starts it when nothing has yet
// for this run of the server (an app that took the server over, or a test
// harness that has no After), and while it runs the probe doesn't call the
// server playable. Each step does nothing when it's already so, so running
// it twice is harmless.

// setupRun is one setup, for one run of the server.
type setupRun struct {
	done     chan struct{}
	cancel   context.CancelFunc
	err      error     // set before done is closed
	finished time.Time // likewise
}

func (r *setupRun) running() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// How long a setup may take in all, how often it asks a server that isn't
// answering yet, and how soon a failed setup is tried again by the probe.
var (
	setupLimit = 20 * time.Minute
	setupPoll  = time.Second
	setupRetry = 2 * time.Minute
)

// runKey names one run of the server: the same process, started once.
func runKey(rec game.Record) string {
	return strconv.Itoa(rec.PID) + "@" + strconv.FormatInt(rec.Started.UnixNano(), 10)
}

// ensureSetup starts the setup for rec's run unless one is running or one
// finished well; one that failed is tried again after setupRetry.
func (s *server) ensureSetup(rec game.Record) *setupRun {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	key := runKey(rec)
	if r := s.setups[key]; r != nil && (r.running() || r.err == nil || time.Since(r.finished) < setupRetry) {
		return r
	}
	// One instance runs one server at a time, so an older run's setup is
	// over: one still waiting for a server that has gone must not set up
	// this run's server for its own world.
	for k, r := range s.setups {
		if k != key {
			r.cancel()
			delete(s.setups, k)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupLimit)
	r := &setupRun{done: make(chan struct{}), cancel: cancel}
	s.setups[key] = r
	go func() {
		defer cancel()
		err := s.setup(ctx, rec)
		if err != nil {
			s.log.Printf("setting the server up: %v", err)
		}
		r.err, r.finished = err, time.Now()
		close(r.done)
	}()
	return r
}

// After runs the setup and waits for it (the online post waits too).
func (s *server) After(ctx context.Context, rec game.Record) (map[string]json.RawMessage, error) {
	r := s.ensureSetup(rec)
	select {
	case <-r.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if r.err != nil {
		return nil, r.err
	}
	w, _ := json.Marshal(rec.World)
	return map[string]json.RawMessage{extraSetUp: w}, nil
}

// Pending says the run's setup never finished, so a copy of the app that
// takes the server over runs it again.
func (s *server) Pending(rec game.Record) bool {
	_, done := rec.Extra[extraSetUp]
	return !done
}

// setup does the steps above.
func (s *server) setup(ctx context.Context, rec game.Record) error {
	c, err := s.waitForAPI(ctx, rec)
	if err != nil {
		return err
	}
	if err := s.logIn(ctx, rec, c); err != nil {
		return err
	}
	if err := s.applySettings(ctx, rec, c); err != nil {
		return err
	}
	return s.loadWorld(ctx, rec, c)
}

// waitForAPI waits until the server's API answers, with a certificate the
// app trusts.
func (s *server) waitForAPI(ctx context.Context, rec game.Record) (*apiClient, error) {
	for {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		c, err := s.client(cctx, rec)
		if err == nil && !c.greeted {
			err = s.checked(rec, c, func() error { _, e := c.healthCheck(cctx); return e })
		}
		cancel()
		if err == nil {
			return c, nil
		}
		var de *distrustError
		var ae *apiError
		if errors.As(err, &de) || (errors.As(err, &ae) && ae.Status == 404) {
			return nil, err // waiting won't help
		}
		if runGone(rec) {
			return nil, errors.New("the Satisfactory server stopped before its API answered")
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("Satisfactory's server API didn't answer: %w", err)
		case <-time.After(setupPoll):
		}
	}
}

// logIn logs in as the administrator, claiming the server first when it
// has never been claimed on this port. c keeps the token.
func (s *server) logIn(ctx context.Context, rec game.Record, c *apiClient) error {
	st := s.store.Get().State
	if st[stateClaimed] != "" && st[stateClaimPort] == itoa(c.port) {
		err := s.passwordLogIn(ctx, rec, c)
		if !errors.Is(err, errUnknownAdmin) {
			return err
		}
		// No password the app knows works: the server may have forgotten
		// its claim (its settings file was deleted), and then it can be
		// claimed again.
	}
	var tok string
	err := s.checked(rec, c, func() (e error) { tok, e = c.passwordlessLogin(ctx, levelInitialAdmin); return e })
	switch {
	case err == nil:
		c.token = tok
		return s.claim(ctx, rec, c)
	case codeOf(err) != codePasswordless:
		return err
	}
	// It is claimed already: by this app on another port with the same
	// password, by the app before its settings were lost, or by someone
	// in the game. A password login tells.
	return s.passwordLogIn(ctx, rec, c)
}

// errUnknownAdmin: the server is claimed with an admin password the app
// doesn't have.
var errUnknownAdmin = errors.New("This Satisfactory server was set up before (in the game's Server Manager, or by another copy of the app) with an admin password the app doesn't know. Type that password under Admin password in Satisfactory's settings (More settings), then start the server again.")

// claim claims a server that has never been claimed. The admin password
// is the one the owner typed, or a new one, and is saved before the claim
// is sent, so a claim whose answer is lost still leaves the app knowing it.
// What the app gave the server before (its name, the friends' password)
// went with the old claim, so it is forgotten at the same time and
// applied again afterwards, even if the answer is lost.
func (s *server) claim(ctx context.Context, rec game.Record, c *apiClient) error {
	var admin string
	err := s.store.Save(func(sv *game.Saved) error {
		ensureMaps(sv)
		if sv.Server["adminPassword"] == "" {
			sv.Server["adminPassword"] = newSecret()
		}
		admin = sv.Server["adminPassword"]
		sv.State[stateAdmin] = admin
		forgetApplied(sv)
		return nil
	})
	if err != nil {
		return err
	}
	name := rec.Server
	if name == "" {
		name = serverName(s.store.Get())
	}
	var tok string
	err = s.checked(rec, c, func() (e error) { tok, e = c.claimServer(ctx, name, admin); return e })
	if codeOf(err) == codeClaimed {
		return s.passwordLogIn(ctx, rec, c) // claimed in between
	}
	if err != nil {
		return err
	}
	c.token = tok
	s.keepToken(itoa(c.port)+"\x00"+admin, tok)
	s.log.Printf("claimed the Satisfactory server as %q", name)
	return s.store.Save(func(sv *game.Saved) error {
		ensureMaps(sv)
		sv.State[stateClaimed] = time.Now().UTC().Format(time.RFC3339)
		sv.State[stateClaimPort] = itoa(c.port)
		sv.State[stateName] = name
		return nil
	})
}

// passwordLogIn logs in to a claimed server with the admin password: the
// one the server had last time, else the one the owner typed, else the
// one the run started with.
func (s *server) passwordLogIn(ctx context.Context, rec game.Record, c *apiClient) error {
	sv := s.store.Get()
	var tries []string
	for _, pw := range []string{sv.State[stateAdmin], sv.Server["adminPassword"], rec.Secrets[secretAdmin]} {
		if pw != "" && !contains(tries, pw) {
			tries = append(tries, pw)
		}
	}
	for _, pw := range tries {
		var tok string
		err := s.checked(rec, c, func() (e error) { tok, e = c.passwordLogin(ctx, levelAdmin, pw); return e })
		if codeOf(err) == codeWrongPass {
			continue
		}
		if err != nil {
			return err
		}
		c.token = tok
		s.keepToken(itoa(c.port)+"\x00"+pw, tok)
		return s.store.Save(func(sv *game.Saved) error {
			ensureMaps(sv)
			sv.State[stateAdmin] = pw
			if sv.Server["adminPassword"] == "" {
				sv.Server["adminPassword"] = pw
			}
			if sv.State[stateClaimed] == "" || sv.State[stateClaimPort] != itoa(c.port) {
				// A server on another port than last time: what it
				// was given on this port, if anything, may differ.
				sv.State[stateClaimed] = time.Now().UTC().Format(time.RFC3339)
				sv.State[stateClaimPort] = itoa(c.port)
				forgetApplied(sv)
			}
			return nil
		})
	}
	return errUnknownAdmin
}

// forgetApplied forgets which server name and friends' password the app
// gave the server, so the next setup gives both again. The server keeps
// them per game port, with its claim (ServerSettings.<port>.sav), so they
// are only known for the port of the claim the app last saw: after the
// app picked new ports, after the port goes back to one used before, or
// after the server forgot its claim, the server there may have no
// friends' password at all, while the panel shows one.
func forgetApplied(sv *game.Saved) {
	delete(sv.State, stateName)
	delete(sv.State, statePassword)
}

// applySettings gives the server what changed since the app last told it:
// the admin password the owner typed, the server name and the friends'
// password (those two as the run started with them).
func (s *server) applySettings(ctx context.Context, rec game.Record, c *apiClient) error {
	sv := s.store.Get()
	have, want := sv.State[stateAdmin], sv.Server["adminPassword"]
	switch {
	case want == "" && have != "":
		// The owner cleared the box: show the password the server has.
		if err := s.store.Save(func(sv *game.Saved) error {
			ensureMaps(sv)
			if sv.Server["adminPassword"] == "" {
				sv.Server["adminPassword"] = have
			}
			return nil
		}); err != nil {
			return err
		}
	case want != "" && want != have:
		var tok string
		err := s.checked(rec, c, func() (e error) { tok, e = c.setAdminPassword(ctx, want); return e })
		if err != nil {
			return fmt.Errorf("couldn't change the server's admin password: %w", err)
		}
		if err := s.store.Save(func(sv *game.Saved) error {
			ensureMaps(sv)
			sv.State[stateAdmin] = want
			return nil
		}); err != nil {
			return err
		}
		s.dropToken()
		if tok == "" {
			// Every earlier token stopped working: log in again.
			err = s.checked(rec, c, func() (e error) { tok, e = c.passwordLogin(ctx, levelAdmin, want); return e })
			if err != nil {
				return err
			}
		}
		c.token = tok
		s.keepToken(itoa(c.port)+"\x00"+want, tok)
		s.log.Print("changed the server's admin password to the one in the settings")
	}

	name := rec.Server
	if name == "" {
		name = serverName(sv)
	}
	if s.store.Get().State[stateName] != name {
		if err := s.checked(rec, c, func() error { return c.renameServer(ctx, name) }); err != nil {
			return fmt.Errorf("couldn't give the server its name: %w", err)
		}
		if err := s.saveState(stateName, name); err != nil {
			return err
		}
	}
	if h := passwordHash(rec.Password); s.store.Get().State[statePassword] != h {
		if err := s.checked(rec, c, func() error { return c.setClientPassword(ctx, rec.Password) }); err != nil {
			return fmt.Errorf("couldn't set the friends' password: %w", err)
		}
		if err := s.saveState(statePassword, h); err != nil {
			return err
		}
	}
	return nil
}

// loadWorld makes sure the run's world is the one being played, and is the
// one the server loads by itself next time. For an imported save, the run's
// world is the file's name and the session is the one inside it (the plan
// has already moved the selection to that session).
func (s *server) loadWorld(ctx context.Context, rec game.Record, c *apiClient) error {
	target := s.worlds.sessionOf(rec.World)
	if target == "" {
		return nil // a server found running without a world: leave it as it is
	}
	var st serverGameState
	if err := s.checked(rec, c, func() (e error) { st, e = c.queryState(ctx); return e }); err != nil {
		return err
	}
	if !st.IsGameRunning || !sameSession(st.ActiveSessionName, target) {
		created := false
		if save, ok := s.worlds.newestSave(target); ok {
			s.log.Printf("loading %q (save %q)", target, save)
			if err := s.checked(rec, c, func() error { return c.loadGame(ctx, save) }); err != nil {
				return fmt.Errorf("couldn't load the world %q: %w", target, err)
			}
		} else {
			start := newWorldOf(rec).StartingLocation
			s.log.Printf("making the new world %q (start: %q)", target, start)
			err := s.checked(rec, c, func() error { return c.createNewGame(ctx, target, start) })
			var ae *apiError
			if err != nil && start != "" && errors.As(err, &ae) && ae.Code != "" {
				// Should a game version name the starting locations
				// differently, a world at a random start beats none.
				s.log.Printf("the server turned down the start %q (%v); letting it choose", start, err)
				err = s.checked(rec, c, func() error { return c.createNewGame(ctx, target, "") })
			}
			if err != nil {
				return fmt.Errorf("couldn't make the world %q: %w", target, err)
			}
			created = true
		}
		if err := s.waitLoaded(ctx, rec, c, target); err != nil {
			return err
		}
		if created {
			// A new world exists only in the server's memory until its
			// first save; saving now puts it on the Worlds page at once.
			if err := s.checked(rec, c, func() error { return c.saveGame(ctx, target) }); err != nil {
				return fmt.Errorf("couldn't save the new world %q: %w", target, err)
			}
		}
		if err := s.checked(rec, c, func() (e error) { st, e = c.queryState(ctx); return e }); err != nil {
			return err
		}
	}
	if !sameSession(st.AutoLoadSessionName, target) {
		if err := s.checked(rec, c, func() error { return c.setAutoLoadSession(ctx, target) }); err != nil {
			return fmt.Errorf("couldn't make %q the world the server loads when it starts: %w", target, err)
		}
	}
	return nil
}

// waitLoaded waits until the server plays target. While it loads, its API
// doesn't answer at all, so failures are expected for a while.
func (s *server) waitLoaded(ctx context.Context, rec game.Record, c *apiClient, target string) error {
	var last error
	for {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var st serverGameState
		err := s.checked(rec, c, func() (e error) { st, e = c.queryState(cctx); return e })
		cancel()
		switch {
		case err == nil && st.IsGameRunning && sameSession(st.ActiveSessionName, target):
			return nil
		case err == nil:
			last = fmt.Errorf("the server is playing %q, not %q", st.ActiveSessionName, target)
		case authFailed(err):
			s.dropToken()
			if lerr := s.passwordLogIn(ctx, rec, c); lerr != nil {
				return lerr
			}
		default:
			last = err
		}
		if err != nil && runGone(rec) {
			return fmt.Errorf("the Satisfactory server stopped while it loaded %q", target)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the world %q didn't finish loading: %v", target, last)
		case <-time.After(setupPoll):
		}
	}
}

// runGone says the run's server process has ended, or its process id now
// belongs to another program (its start time differs). Tests replace it.
var runGone = func(rec game.Record) bool {
	if rec.PID <= 0 {
		return false
	}
	p, err := platform.OpenProc(rec.PID)
	if err != nil {
		return true
	}
	defer p.Close()
	if p.Exited() {
		return true
	}
	if !rec.Started.IsZero() && !p.StartTime().IsZero() {
		if d := p.StartTime().Sub(rec.Started); d > 2*time.Second || d < -2*time.Second {
			return true
		}
	}
	return false
}

// newWorldOf is what the plan noted for a new world.
func newWorldOf(rec game.Record) newWorld {
	var nw newWorld
	if b, ok := rec.Extra[extraNewWorld]; ok {
		json.Unmarshal(b, &nw)
	}
	return nw
}

// sameSession compares session names as the server does: letter case
// apart, since a session's saves are files named after it.
func sameSession(a, b string) bool { return strings.EqualFold(a, b) }

// saveState sets one State key.
func (s *server) saveState(key, value string) error {
	return s.store.Save(func(sv *game.Saved) error {
		ensureMaps(sv)
		sv.State[key] = value
		return nil
	})
}

func ensureMaps(sv *game.Saved) {
	if sv.Server == nil {
		sv.Server = game.Values{}
	}
	if sv.State == nil {
		sv.State = game.Values{}
	}
}

// passwordHash notes which friends' password the server was given without
// keeping it twice.
func passwordHash(pw string) string {
	sum := sha256.Sum256([]byte("satisfactory client password:" + pw))
	return hex.EncodeToString(sum[:8])
}

// newSecret makes an admin password: letters and digits only, none that
// look alike, so the owner can read it out of the settings and type it in
// the game. Tests replace it.
var newSecret = func() string {
	const letters = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 20)
	rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
