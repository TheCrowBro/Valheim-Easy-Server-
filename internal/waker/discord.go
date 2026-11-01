package waker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

const (
	apiURL     = "https://discord.com/api/v10"
	gatewayURL = "wss://gateway.discord.gg"
	userAgent  = "DiscordBot (https://github.com/TheCrowBro/Valheim-Easy-Server-, 1)"
)

// apiError is Discord saying no.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("Discord answered %d: %s", e.Status, e.Message) }

// rest calls Discord's API. Calls about an interaction carry its token in
// the path instead of the bot's.
func (b *Bot) rest(ctx context.Context, method, path string, auth bool, body, out any) error {
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, b.api+path, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if auth {
			req.Header.Set("Authorization", "Bot "+b.Config.Token)
		}
		res, err := b.http.Do(req)
		if err != nil {
			return err
		}
		reply, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			var rl struct {
				RetryAfter float64 `json:"retry_after"`
			}
			json.Unmarshal(reply, &rl)
			wait := min(max(time.Duration(rl.RetryAfter*float64(time.Second)), 500*time.Millisecond), 10*time.Second)
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if res.StatusCode >= 300 {
			var e struct {
				Message string `json:"message"`
			}
			json.Unmarshal(reply, &e)
			if e.Message == "" {
				e.Message = http.StatusText(res.StatusCode)
			}
			return &apiError{Status: res.StatusCode, Message: e.Message}
		}
		if out != nil && len(reply) > 0 {
			return json.Unmarshal(reply, out)
		}
		return nil
	}
}

// payload is a message on Discord's gateway.
type payload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  int64           `json:"s"`
	T  string          `json:"t"`
}

var errReconnect = errors.New("Discord asked to reconnect")

// session is one connection to Discord's gateway, from hello to its end.
// It resumes the previous session when it can, so nothing is missed.
func (b *Bot) session(ctx context.Context) error {
	b.mu.Lock()
	url, resume := b.gateway, false
	if b.sessionID != "" && b.resumeURL != "" {
		url, resume = b.resumeURL, true
	}
	b.mu.Unlock()
	cfg, err := websocket.NewConfig(url+"/?v=10&encoding=json", "https://localhost/")
	if err != nil {
		return err
	}
	cfg.Dialer = &net.Dialer{Timeout: 20 * time.Second}
	cfg.Header.Set("User-Agent", userAgent)
	conn, err := cfg.DialContext(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-sctx.Done()
		conn.Close()
	}()

	var wmu sync.Mutex
	send := func(op int, d any) error {
		wmu.Lock()
		defer wmu.Unlock()
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return websocket.JSON.Send(conn, map[string]any{"op": op, "d": d})
	}
	read := func(timeout time.Duration) (payload, error) {
		conn.SetReadDeadline(time.Now().Add(timeout))
		var p payload
		err := websocket.JSON.Receive(conn, &p)
		return p, err
	}

	hello, err := read(30 * time.Second)
	if err != nil {
		return err
	}
	if hello.Op != 10 {
		return fmt.Errorf("Discord's gateway started with op %d", hello.Op)
	}
	var h struct {
		Interval float64 `json:"heartbeat_interval"`
	}
	json.Unmarshal(hello.D, &h)
	interval := time.Duration(h.Interval * float64(time.Millisecond))
	if interval <= 0 {
		interval = 41250 * time.Millisecond
	}
	if resume {
		b.mu.Lock()
		d := map[string]any{"token": b.Config.Token, "session_id": b.sessionID, "seq": b.seq}
		b.mu.Unlock()
		err = send(6, d)
	} else {
		err = send(2, map[string]any{
			"token":      b.Config.Token,
			"intents":    1, // GUILDS: to hear which Discord servers it's in
			"properties": map[string]string{"os": "linux", "browser": "valheim-waker", "device": "valheim-waker"},
		})
	}
	if err != nil {
		return err
	}

	// Heartbeats keep the connection open. Without an answer to the last
	// one, the connection is dead and closing it starts a new one.
	var acked atomic.Bool
	acked.Store(true)
	beat := func() error {
		b.mu.Lock()
		var d any
		if b.seq > 0 {
			d = b.seq
		}
		b.mu.Unlock()
		return send(1, d)
	}
	go func() {
		t := time.NewTimer(time.Duration(rand.Int64N(int64(interval))))
		defer t.Stop()
		for {
			select {
			case <-sctx.Done():
				return
			case <-t.C:
			}
			if !acked.Swap(false) || beat() != nil {
				conn.Close()
				return
			}
			t.Reset(interval)
		}
	}()

	for {
		p, err := read(interval + 30*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		switch p.Op {
		case 0:
			if p.S > 0 {
				b.mu.Lock()
				b.seq = p.S
				b.mu.Unlock()
			}
			b.dispatch(ctx, p.T, p.D)
		case 1:
			beat()
		case 7:
			return errReconnect
		case 9: // the session can't go on; it says whether it can be resumed
			var resumable bool
			json.Unmarshal(p.D, &resumable)
			if !resumable {
				b.mu.Lock()
				b.sessionID, b.resumeURL, b.seq = "", "", 0
				b.mu.Unlock()
			}
			select {
			case <-time.After(time.Second + rand.N(4*time.Second)):
			case <-ctx.Done():
			}
			return errors.New("Discord ended the session")
		case 11:
			acked.Store(true)
		}
	}
}
