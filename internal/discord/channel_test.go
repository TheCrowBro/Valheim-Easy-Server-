package discord

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A bot reads a channel's messages after a given one, says who sent each,
// and tells a channel it may not read from Discord being out of reach.
func TestBotMessages(t *testing.T) {
	var query, auth, agent atomic.Value
	var limited atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		agent.Store(r.Header.Get("User-Agent"))
		switch r.URL.Path {
		case "/api/v10/channels/77/messages":
			query.Store(r.URL.RawQuery)
			io.WriteString(w, `[
				{"id": "300", "author": {"id": "u1", "username": "alex"}, "content": ""},
				{"id": "290", "author": {"id": "b1", "bot": true}},
				{"id": "280", "webhook_id": "h1", "author": {"id": "h1", "bot": true}}]`)
		case "/api/v10/channels/77":
			io.WriteString(w, `{"id": "77", "type": 0, "name": "windrose"}`)
		case "/api/v10/channels/hidden/messages":
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"message": "Missing Access", "code": 50001}`)
		case "/api/v10/channels/gone/messages":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message": "Unknown Channel", "code": 10003}`)
		case "/api/v10/channels/trouble/messages":
			w.WriteHeader(http.StatusBadGateway)
		case "/api/v10/channels/slow/messages":
			if limited.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, `{"message": "You are being rate limited.", "retry_after": 0.05}`)
				return
			}
			io.WriteString(w, `[]`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message": "401: Unauthorized", "code": 0}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	bot := Bot{Token: "tok", API: srv.URL + "/api/v10/"}

	msgs, err := bot.Messages(ctx, "77", "250", 20)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	if q := query.Load().(string); q != "after=250&limit=20" {
		t.Errorf("query %q", q)
	}
	if a := auth.Load().(string); a != "Bot tok" {
		t.Errorf("authorization %q", a)
	}
	if a := agent.Load().(string); !strings.HasPrefix(a, "DiscordBot (") {
		t.Errorf("user agent %q", a)
	}
	if !msgs[0].FromPerson() || msgs[1].FromPerson() || msgs[2].FromPerson() || msgs[0].ID != "300" {
		t.Errorf("who sent them: %+v", msgs)
	}
	if name, err := bot.ChannelName(ctx, "77"); err != nil || name != "windrose" {
		t.Errorf("name = %q, %v", name, err)
	}

	for ch, want := range map[string]struct {
		status    int
		temporary bool
		msg       string
	}{
		"hidden":  {403, false, "isn't allowed to read"},
		"gone":    {404, false, "doesn't know that channel"},
		"trouble": {502, true, "Discord had a problem"},
		"other":   {401, false, "didn't accept the bot's token"},
	} {
		_, err := bot.Messages(ctx, ch, "", 20)
		var de *Error
		if !errors.As(err, &de) || de.Status != want.status || de.Temporary != want.temporary || !strings.Contains(de.Msg, want.msg) {
			t.Errorf("%s: %v (%+v)", ch, err, de)
		}
	}
	// Asked to slow down: wait as told and try once more.
	if _, err := bot.Messages(ctx, "slow", "", 20); err != nil || limited.Load() != 2 {
		t.Errorf("rate limited: %v after %d calls", err, limited.Load())
	}

	// No token: nothing is asked.
	if _, err := (Bot{API: srv.URL}).Messages(ctx, "77", "", 20); err == nil {
		t.Error("read without a token")
	}
	// Discord out of reach: worth trying later.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	var de *Error
	if _, err := (Bot{Token: "tok", API: dead.URL}).Messages(ctx, "77", "", 20); !errors.As(err, &de) || !de.Temporary || de.Status != 0 {
		t.Errorf("unreachable: %v", err)
	}
}

// The limit stays within what Discord accepts.
func TestBotMessagesLimit(t *testing.T) {
	var query atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.RawQuery)
		io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	bot := Bot{Token: "tok", API: srv.URL}
	for limit, want := range map[int]string{500: "limit=100", 0: "limit=1"} {
		if _, err := bot.Messages(context.Background(), "77", "", limit); err != nil {
			t.Fatal(err)
		}
		if q := query.Load().(string); q != want {
			t.Errorf("limit %d: query %q", limit, q)
		}
	}
}

// A snowflake made for a time is just below the ids of messages made then,
// so "after" it means "posted after that time".
func TestSnowflakes(t *testing.T) {
	// An id and the time it carries (Discord's documented layout: the
	// milliseconds since 2015 in the top 42 bits).
	const id = "1311755374364540969" // 2024-11-28 18:07:32.484 UTC
	at := time.Date(2024, 11, 28, 18, 7, 32, 484e6, time.UTC)
	if !Later(id, SnowflakeAt(at.Add(-time.Millisecond))) || Later(id, SnowflakeAt(at.Add(time.Millisecond))) {
		t.Errorf("id %s against %s and %s", id, SnowflakeAt(at.Add(-time.Millisecond)), SnowflakeAt(at.Add(time.Millisecond)))
	}
	if got := SnowflakeAt(time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)); got != "0" {
		t.Errorf("before Discord's ids begin: %s", got)
	}
	if !Later("10", "9") || Later("9", "10") || Later("10", "10") {
		t.Error("ids are compared as numbers")
	}
	if Later("x", "1") || !Later("1", "") || !Later("1", "x") {
		t.Error("an id that isn't a number")
	}
}

// PostID gives the new message's id; Post sends exactly the same.
func TestPostID(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, r.URL.RawQuery+" "+string(b))
		if strings.Contains(r.URL.Path, "noid") {
			io.WriteString(w, `{}`)
			return
		}
		io.WriteString(w, `{"id": "1311755374364540969", "type": 0}`)
	}))
	defer srv.Close()
	ctx := context.Background()
	id, err := PostID(ctx, nil, srv.URL+"/api/webhooks/1/x", "hello")
	if err != nil || id != "1311755374364540969" {
		t.Fatalf("id %q, %v", id, err)
	}
	if err := Post(ctx, nil, srv.URL+"/api/webhooks/1/x", "hello"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("Post and PostID sent %q", bodies)
	}
	if id, err := PostID(ctx, nil, srv.URL+"/api/webhooks/1/noid", "hello"); err != nil || id != "" {
		t.Fatalf("no id: %q, %v", id, err)
	}
}
