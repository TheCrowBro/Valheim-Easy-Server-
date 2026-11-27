package discord

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/webhooks/1/good":
			io.WriteString(w, `{"id": "1", "type": 1, "guild_id": "4242", "channel_id": "77"}`)
		case "/api/webhooks/1/nochannel":
			io.WriteString(w, `{"id": "1", "type": 1, "guild_id": "4242"}`)
		case "/api/webhooks/1/noguild":
			io.WriteString(w, `{"id": "1", "type": 1, "channel_id": "77"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message": "Unknown Webhook", "code": 10015}`)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	if g, c, err := Webhook(ctx, nil, srv.URL+"/api/webhooks/1/good"); err != nil || g != "4242" || c != "77" {
		t.Fatalf("Webhook = %q %q, %v", g, c, err)
	}
	for path, want := range map[string]string{
		"gone":      "webhook link doesn't work",
		"nochannel": "which channel",
		"noguild":   "which Discord server",
	} {
		if _, _, err := Webhook(ctx, nil, srv.URL+"/api/webhooks/1/"+path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", path, err, want)
		}
	}
	// Guild answers as it always did for the same webhook.
	if g, err := Guild(ctx, nil, srv.URL+"/api/webhooks/1/good"); err != nil || g != "4242" {
		t.Fatalf("Guild = %q, %v", g, err)
	}
}
