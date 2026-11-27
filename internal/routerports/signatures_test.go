package routerports

import (
	"context"
	"net"
	"time"
)

// These pin the phase 0' contract: the manager (router.go, stream N's) and
// its status are written against them while this package is filled in, so
// changing one needs the lead, and must change this file too.
var (
	_ func(context.Context, net.IP, net.IP) (Router, error)               = Find
	_ func(net.IP, net.IP) string                                         = NAT
	_ func(context.Context, string) ([]SteamServer, error)                = SteamSees
	_ func(Router) string                                                 = Router.Kind
	_ func(Router, context.Context) (net.IP, error)                       = Router.ExternalIP
	_ func(Router, context.Context, net.IP, Mapping, time.Duration) error = Router.Map
	_ func(Router, context.Context, net.IP, Mapping) error                = Router.Unmap
	_                                                                     = Mapping{Proto: "tcp", Port: 25565, Desc: "Easy Game Server minecraft game"}
	_                                                                     = []error{ErrForwardedByHand, ErrConflict, ErrNoRouter}
)
