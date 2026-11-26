package waker

// What the waker and the app share across versions
// (docs/dev/multigame/spec.md 1.2 and 6.4): the /valheim command, the
// settings file the app writes, and the status file the app reads. After an
// update or a rollback, the app may talk to a waker from another version
// until it puts its own on the JetKVM, so none of these change; new fields
// are only ever added. Each value is compared with its literal: renaming
// one fails here.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestIdentity(t *testing.T) {
	if commandName != "valheim" {
		t.Errorf("the command = /%s, want /valheim", commandName)
	}
	if userAgent != "DiscordBot (https://github.com/TheCrowBro/Valheim-Easy-Server-, 1)" {
		t.Errorf("user agent = %q", userAgent)
	}

	at := time.Date(2026, 11, 20, 18, 30, 0, 0, time.UTC)
	// The settings file: every key today's app writes, with the same type.
	// Only "games" (the commands for several games) may be added.
	cfg := Config{Token: "token", MAC: "3C:07:54:5A:21:9F", Query: "192.168.1.20:2457", Name: "Viking Night",
		Guild: "112233", API: "http://127.0.0.1:1/api", Gateway: "ws://127.0.0.1:1", Targets: []string{"127.0.0.1:9"}}
	checkKeys(t, "the settings file", cfg, map[string]string{
		"token": "string", "mac": "string", "query": "string", "name": "string", "guild": "string",
		"api": "string", "gateway": "string", "targets": "array",
	}, map[string]bool{"games": true})

	// The status file: every key today's waker writes, with the same type.
	// An app reads it whatever it adds.
	st := Status{Version: "build-7", Connected: true, Bot: "Valheim Bot", Servers: 1, LastWake: &at,
		WokenBy: "Astrid", Error: "no", Updated: at}
	checkKeys(t, "the status file", st, map[string]string{
		"version": "string", "connected": "bool", "bot": "string", "servers": "number",
		"lastWake": "string", "wokenBy": "string", "error": "string", "updated": "string",
	}, nil)

	// Files written by today's app and waker read back the same.
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{
  "token": "token",
  "mac": "3C:07:54:5A:21:9F",
  "query": "192.168.1.20:2457",
  "name": "Viking Night",
  "guild": "112233",
  "api": "http://127.0.0.1:1/api",
  "gateway": "ws://127.0.0.1:1",
  "targets": ["127.0.0.1:9"]
}`), 0o600)
	if got, err := LoadConfig(path); err != nil || !reflect.DeepEqual(got, cfg) {
		t.Errorf("today's settings file reads as %+v (%v), want %+v", got, err, cfg)
	}
	var back Status
	if err := json.Unmarshal([]byte(`{"version":"build-7","connected":true,"bot":"Valheim Bot","servers":1,"lastWake":"2026-11-20T18:30:00Z","wokenBy":"Astrid","error":"no","updated":"2026-11-20T18:30:00Z"}`), &back); err != nil ||
		back.Version != st.Version || !back.Connected || back.Bot != st.Bot || back.Servers != 1 || back.LastWake == nil ||
		!back.LastWake.Equal(at) || back.WokenBy != st.WokenBy || back.Error != st.Error || !back.Updated.Equal(at) {
		t.Errorf("today's status file reads as %+v (%v)", back, err)
	}
}

// checkKeys checks that v, as JSON, has each key in want with its JSON
// type, and no other key unless extra allows it (any, when extra is nil).
func checkKeys(t *testing.T, what string, v any, want map[string]string, extra map[string]bool) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for k, kind := range want {
		if g, ok := got[k]; !ok {
			t.Errorf("%s has no %q any more: an older copy needs it", what, k)
		} else if jsonKind(g) != kind {
			t.Errorf("%s: %q is a %s, want a %s", what, k, jsonKind(g), kind)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok && extra != nil && !extra[k] {
			t.Errorf("%s has a new key %q that isn't on the allowed list", what, k)
		}
	}
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}
