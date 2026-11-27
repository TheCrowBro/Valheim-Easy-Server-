package palworld

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"valheim-easy-server/internal/game"
)

// The server's REST API (the docs' "Palworld REST API", FACTS.md): plain
// HTTP on RESTAPIPort under /v1/api/, basic authentication as "admin" with
// AdminPassword. GET info and players answer JSON; POST save and shutdown
// answer 200 with a short text. The app uses those four. The docs say it is
// not made to face the internet, which is why its port is private and its
// password new for every run.

// restInfo is GET /v1/api/info.
type restInfo struct {
	Version     string `json:"version"`
	ServerName  string `json:"servername"`
	Description string `json:"description"`
	WorldGUID   string `json:"worldguid"`
}

// restPlayer is one entry of GET /v1/api/players.
type restPlayer struct {
	Name          string  `json:"name"`
	AccountName   string  `json:"accountName"`
	PlayerID      string  `json:"playerId"`
	UserID        string  `json:"userId"`
	IP            string  `json:"ip"`
	Ping          float64 `json:"ping"`
	LocationX     float64 `json:"location_x"`
	LocationY     float64 `json:"location_y"`
	Level         int     `json:"level"`
	BuildingCount int     `json:"building_count"`
}

// restClient asks one running server.
type restClient struct {
	port     int
	password string
	http     *http.Client
}

// localHTTP talks to the server on this computer only: never through a
// proxy the system may set, and never waiting long, because the probe has
// 3 seconds in all and the stop chain moves on when the API is gone. Each
// call has a connection of its own: one kept from an earlier run of the
// server, on the same port, could carry the stop's save into a dead
// connection.
var localHTTP = &http.Client{
	Transport: &http.Transport{
		Proxy:             nil,
		DialContext:       (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		DisableKeepAlives: true,
	},
	// Each call also has its context's deadline; this is the most any
	// one may take when the caller gave none.
	Timeout: 2 * time.Minute,
}

// restFor is the API of the run in rec: its port and password are in the
// run record, so the probe and the stop work after a takeover too.
func restFor(rec game.Record) (*restClient, error) {
	port := rec.Ports["rest"]
	if port <= 0 {
		return nil, errors.New("the run record has no REST API port")
	}
	return &restClient{port: port, password: rec.Secrets[restSecret], http: localHTTP}, nil
}

// errUnauthorized: the server turned down the run's admin password. A
// world's own WorldOption.sav can override PalWorldSettings.ini (FACTS.md),
// which is the likeliest way that happens.
var errUnauthorized = errors.New("Palworld's REST API turned down the app's admin password (a WorldOption.sav in the world's folder may be overriding the server's settings)")

// call sends one request and returns the answer's body.
func (c *restClient) call(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	url := "http://127.0.0.1:" + strconv.Itoa(c.port) + "/v1/api/" + endpoint
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(restUser, c.password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	} else if method == http.MethodPost {
		// A POST without a body still says so: some servers turn down a
		// POST with no Content-Length at all (FACTS.md).
		req.ContentLength = 0
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errUnauthorized
	case resp.StatusCode != http.StatusOK:
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg != "" {
			msg = ": " + msg
		}
		return nil, fmt.Errorf("Palworld's REST API answered %s to %s%s", resp.Status, endpoint, msg)
	}
	return b, err
}

func (c *restClient) getJSON(ctx context.Context, endpoint string, v any) error {
	b, err := c.call(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("Palworld's REST API gave an answer to %s the app can't read: %w", endpoint, err)
	}
	return nil
}

func (c *restClient) info(ctx context.Context) (restInfo, error) {
	var in restInfo
	return in, c.getJSON(ctx, "info", &in)
}

func (c *restClient) players(ctx context.Context) ([]restPlayer, error) {
	var out struct {
		Players []restPlayer `json:"players"`
	}
	if err := c.getJSON(ctx, "players", &out); err != nil {
		return nil, err
	}
	if out.Players == nil {
		out.Players = []restPlayer{}
	}
	return out.Players, nil
}

// save asks the server to save the world now.
func (c *restClient) save(ctx context.Context) error {
	_, err := c.call(ctx, http.MethodPost, "save", nil)
	return err
}

// shutdown asks the server to stop after wait seconds, telling players
// message first. The docs make waittime required.
func (c *restClient) shutdown(ctx context.Context, wait int, message string) error {
	_, err := c.call(ctx, http.MethodPost, "shutdown", map[string]any{"waittime": wait, "message": message})
	return err
}

// Probe asks the REST API who is on and which version runs. An answer
// means the server is up (spec 2.6): the Monitor marks it online.
//
// Every player the API lists counts, but only those with a name are
// named: a player still loading in, or still making their character, is
// listed before they have one (FACTS.md).
func (s *server) Probe(ctx context.Context, rec game.Record) (game.ProbeResult, error) {
	c, err := restFor(rec)
	if err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	list, perr := c.players(ctx)
	in, ierr := c.info(ctx)
	if perr != nil && ierr != nil {
		return game.ProbeResult{Count: -1}, perr
	}
	res := game.ProbeResult{Up: true, Count: -1, Version: in.Version}
	if perr == nil {
		res.Count = len(list)
		res.Names = []string{}
		for _, p := range list {
			if n := strings.TrimSpace(p.Name); n != "" {
				res.Names = append(res.Names, n)
			}
		}
	}
	return res, nil
}

// Stop asks the REST API to save and then shut down, which is the docs'
// way; Ctrl+C is the fallback when the API doesn't answer. Ctrl+C is sent
// once: Unreal Engine programs may take a second one as "quit now", which
// could cut short a save on the way out.
func (s *server) Stop(rec game.Record) []game.StopStep {
	return []game.StopStep{
		{Kind: "call", Call: func(ctx context.Context) error { return restStop(ctx, rec) }, Wait: stopWait},
		{Kind: "interrupt", Wait: interruptWait,
			Say: "Palworld's REST API didn't answer, so the server is being stopped with Ctrl+C. It may not save on the way out: what was played since its last autosave (every 30 seconds) can be lost."},
	}
}

// restStop saves, then shuts the server down a second later.
//
// A save that fails because nothing listens (the server is gone or its API
// never started) or the password is wrong gives that error, and the chain
// goes on to Ctrl+C at once. A save that runs out of time (a big world can
// take longer than the caller waits) is still followed by the shutdown: it
// is the server's own clean way out, which Ctrl+C may not be.
func restStop(ctx context.Context, rec game.Record) error {
	c, err := restFor(rec)
	if err != nil {
		return err
	}
	if err := c.save(ctx); err != nil && (errors.Is(err, errUnauthorized) || refused(err)) {
		return err
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return c.shutdown(sctx, 1, "The server is stopping.")
}

// refused says the connection was turned down: nothing listens there.
func refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "refused")
}
