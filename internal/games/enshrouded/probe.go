package enshrouded

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"valheim-easy-server/internal/a2s"
	"valheim-easy-server/internal/game"
)

// Probe asks the server's Steam query port about itself (A2S_INFO), on
// this computer, as Steam's server list does. Hosting tools count
// Enshrouded's players this way (FACTS.md, "The query port"): the answer
// has how many are on, but not who.
//
// The port comes from the run record, so it works after a takeover and
// asks the port the server really started on, not one changed since.
func (s *server) Probe(ctx context.Context, rec game.Record) (game.ProbeResult, error) {
	port := rec.Ports[roleQuery]
	if port == 0 {
		port = rec.Port
	}
	if port == 0 {
		return game.ProbeResult{Count: -1}, errors.New("the run record has no port")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	}
	info, err := a2s.Query(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return game.ProbeResult{Count: -1}, err
	}
	return game.ProbeResult{Up: true, Count: max(info.Players-info.Bots, 0), Version: info.Version}, nil
}
