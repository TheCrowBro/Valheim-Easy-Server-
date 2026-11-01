package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProblem(t *testing.T) {
	for link, want := range map[string]string{
		"https://discord.com/api/webhooks/123456/AbC_d-9":             "",
		"https://discordapp.com/api/webhooks/123456/AbC/":             "",
		"https://canary.discord.com/api/v10/webhooks/1/x?thread_id=5": "",
		"":                                                  "Paste the webhook link",
		"https://discord.com/channels/111/222":              "link to the channel",
		"https://discord.gg/abcdef":                         "invite link",
		"https://discord.com/invite/abcdef":                 "invite link",
		"http://discord.com/api/webhooks/1/x":               "isn't a Discord webhook link",
		"https://evil.example/api/webhooks/1/x":             "isn't a Discord webhook link",
		"https://discord.com.evil.example/api/webhooks/1/x": "isn't a Discord webhook link",
		"https://discord.com/api/webhooks/1":                "isn't a Discord webhook link",
		"discord.com/api/webhooks/1/x":                      "isn't a Discord webhook link",
		"not a link at all":                                 "isn't a Discord webhook link",
		"https://discord.com/api/webhooks/1/x y":            "isn't a Discord webhook link",
	} {
		got := Problem(link)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("Problem(%q) = %q, want %q", link, got, want)
		}
	}
}

func TestEscape(t *testing.T) {
	if got := Escape("my_world *1* `x` a|b [c](d) <@1> ~s~ \\"); got != `my\_world \*1\* \`+"`"+`x\`+"`"+` a\|b \[c](d) \<@1> \~s\~ \\` {
		t.Errorf("Escape: %s", got)
	}
	if got := Escape("fjord-4821"); got != "fjord-4821" {
		t.Errorf("a plain password changed: %s", got)
	}
}

func TestPost(t *testing.T) {
	var got struct {
		Content         string         `json:"content"`
		AllowedMentions map[string]any `json:"allowed_mentions"`
		Flags           int            `json:"flags"`
	}
	var query, agent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, agent = r.URL.RawQuery, r.Header.Get("User-Agent")
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"id":"1"}`))
	}))
	defer srv.Close()
	if err := Post(context.Background(), nil, srv.URL+"/api/webhooks/1/x?thread_id=7", "hello @everyone"); err != nil {
		t.Fatal(err)
	}
	if got.Content != "hello @everyone" || got.Flags != 4 || got.AllowedMentions == nil {
		t.Errorf("sent %+v", got)
	}
	if parse, ok := got.AllowedMentions["parse"].([]any); !ok || len(parse) != 0 {
		t.Errorf("mentions would ping: %v", got.AllowedMentions)
	}
	if !strings.Contains(query, "wait=true") || !strings.Contains(query, "thread_id=7") {
		t.Errorf("query %q", query)
	}
	if !strings.HasPrefix(agent, "DiscordBot (") {
		t.Errorf("user agent %q", agent)
	}
}

func TestPostErrors(t *testing.T) {
	var calls atomic.Int32
	reply := func(code int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
			w.Write([]byte(body))
		}))
	}
	for _, c := range []struct {
		code      int
		body      string
		msg       string
		temporary bool
	}{
		{404, `{"message": "Unknown Webhook", "code": 10015}`, "webhook link doesn't work", false},
		{401, `{"message": "Invalid Webhook Token", "code": 50027}`, "webhook link doesn't work", false},
		{400, `{"message": "Cannot send an empty message", "code": 50006}`, "(Cannot send an empty message)", false},
		{502, `bad gateway`, "Discord had a problem (error 502)", true},
	} {
		srv := reply(c.code, c.body)
		err := Post(context.Background(), nil, srv.URL+"/api/webhooks/1/x", "hi")
		srv.Close()
		var de *Error
		if !errors.As(err, &de) || !strings.Contains(de.Msg, c.msg) || de.Temporary != c.temporary {
			t.Errorf("%d: got %v", c.code, err)
		}
	}

	// Asked to slow down: wait as told and try once more.
	calls.Store(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message": "You are being rate limited.", "retry_after": 0.05, "global": false}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if err := Post(context.Background(), nil, srv.URL+"/api/webhooks/1/x", "hi"); err != nil || calls.Load() != 2 {
		t.Errorf("rate limited: %v after %d calls", err, calls.Load())
	}

	// Nothing listening: worth trying again later.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	var de *Error
	if err := Post(context.Background(), nil, dead.URL+"/api/webhooks/1/x", "hi"); !errors.As(err, &de) || !de.Temporary {
		t.Errorf("unreachable: %v", err)
	}
}

func TestGuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/webhooks/1/good":
			io.WriteString(w, `{"id": "1", "type": 1, "guild_id": "4242", "channel_id": "77"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message": "Unknown Webhook", "code": 10015}`)
		}
	}))
	defer srv.Close()
	if g, err := Guild(context.Background(), nil, srv.URL+"/api/webhooks/1/good"); err != nil || g != "4242" {
		t.Fatalf("guild = %q, %v", g, err)
	}
	if _, err := Guild(context.Background(), nil, srv.URL+"/api/webhooks/1/gone"); err == nil || !strings.Contains(err.Error(), "webhook link doesn't work") {
		t.Fatalf("err = %v", err)
	}
}
