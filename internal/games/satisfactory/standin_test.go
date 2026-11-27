package satisfactory

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/game/gametest"
)

// The server keeps its certificate and its claim from one start to the
// next (in ServerSettings.<port>.sav), so the second start logs in with
// the admin password, without claiming again, and the key the app pinned
// still matches. The fingerprint the server writes in its log is the
// key's, in the app's own notation.
func TestStandInKeepsClaimAndCertificate(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{World: "Island"})
	s.WaitEvent("online", "", 30*time.Second)
	st := s.Env().Store.Get().State
	pin := st[stateCert]
	if pin == "" || st[stateClaimed] == "" {
		t.Fatalf("State after the first start = %v", st)
	}
	if got := s.State().Extra[extraFingerprint]; got != pin {
		t.Errorf("the log's fingerprint %q isn't the pinned key %q", got, pin)
	}
	if _, err := os.Stat(s.Server().(*server).settingsFile(s.Record().Ports["game"])); err != nil {
		t.Errorf("the server's settings file: %v", err)
	}
	s.Stop()
	claimed := st[stateClaimed]

	s.StartAgain()
	s.WaitEvent("online", "", 30*time.Second)
	st = s.Env().Store.Get().State
	if st[stateCert] != pin || st[stateClaimed] != claimed {
		t.Errorf("after a restart: key %q (was %q), claimed %q (was %q)", st[stateCert], pin, st[stateClaimed], claimed)
	}
	if b, err := os.ReadFile(s.Plan().LogPath); err != nil || strings.Contains(string(b), "Server claimed") || strings.Contains(string(b), "Generated a new Self-Signed") {
		t.Errorf("the second run claimed again or made a new certificate (%v)", err)
	}
	s.Stop()
}

// The friends' password from the settings is given to the server: without
// it, nobody gets in.
func TestStandInFriendsPassword(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{World: "Island",
		Change: func(sv *game.Saved) { sv.Server["password"] = "letmein" }})
	s.WaitEvent("online", "", 30*time.Second)
	srv := s.Server().(*server)
	rec := s.Record()
	c, err := srv.client(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.passwordlessLogin(ctx, "Client"); codeOf(err) != codePasswordless {
		t.Errorf("passwordless login with a friends' password set: %v", err)
	}
	if _, err := c.passwordLogin(ctx, "Client", "letmein"); err != nil {
		t.Errorf("login with the friends' password: %v", err)
	}
	s.Stop()
}

// The stand-in is as strict as the real server about CreateNewGame (every
// newGameData field), so a call the real server would refuse fails here
// too; and it answers nothing secret without a token.
func TestStandInStrictAPI(t *testing.T) {
	s := gametest.Start(t, Game{}, gametest.Options{World: "Island"})
	s.WaitEvent("online", "", 30*time.Second)
	srv := s.Server().(*server)
	rec := s.Record()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := srv.client(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.queryState(ctx); err == nil {
		t.Errorf("QueryServerState answered without a token")
	}
	err = srv.asAdmin(ctx, rec, func(c *apiClient) error {
		return c.call(ctx, "CreateNewGame", map[string]any{"newGameData": map[string]any{
			"sessionName": "x", "mapName": "", "startingLocation": "", "bSkipOnboarding": true,
			"advancedGameSettings": map[string]string{}, "customOptionsOnlyForModding": map[string]string{}}}, nil)
	})
	if codeOf(err) != "missing_params" || !strings.Contains(err.Error(), "GameModeSettings") {
		t.Errorf("CreateNewGame without gameModeSettings: %v", err)
	}
	s.Stop()
}
