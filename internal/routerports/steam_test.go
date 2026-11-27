package routerports

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SteamSees reads Steam's GetServersAtAddress answer (in the real API's
// shape), asking for the address given and nothing else.
func TestSteamSees(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		switch r.URL.Query().Get("addr") {
		case "203.0.113.25":
			w.Write([]byte(`{"response":{"success":true,"servers":[{"addr":"203.0.113.25:2457","gmsindex":-1,` +
				`"steamid":"90213456789012345","appid":892970,"gamedir":"valheim","region":-1,"secure":true,` +
				`"lan":false,"gameport":2456,"specport":0}]}}`))
		case "203.0.113.26":
			w.Write([]byte(`{"response":{"success":true,"servers":[]}}`))
		default:
			w.Write([]byte(`{"response":{"success":false,"message":"Invalid IP address"}}`))
		}
	}))
	defer srv.Close()
	old := steamURL
	steamURL = srv.URL + "/ISteamApps/GetServersAtAddress/v1/"
	defer func() { steamURL = old }()

	got, err := SteamSees(ctx(t), "203.0.113.25")
	if err != nil {
		t.Fatal(err)
	}
	if asked != "addr=203.0.113.25" {
		t.Fatalf("asked %q", asked)
	}
	want := SteamServer{Addr: "203.0.113.25:2457", GamePort: 2456, AppID: 892970, GameDir: "valheim",
		SteamID: "90213456789012345", Secure: true}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("servers = %+v", got)
	}
	if got, err := SteamSees(ctx(t), "203.0.113.26"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("none: %v, %v", got, err)
	}
	if _, err := SteamSees(ctx(t), "198.51.100.1"); err == nil || !strings.Contains(err.Error(), "Invalid IP address") {
		t.Fatalf("refused: %v", err)
	}
	// Only an address is ever sent.
	if _, err := SteamSees(ctx(t), "1.2.3.4&key=x"); err == nil {
		t.Fatal("a value that isn't an address was sent")
	}
}
