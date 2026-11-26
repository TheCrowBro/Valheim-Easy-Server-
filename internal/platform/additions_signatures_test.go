package platform

import (
	"context"
	"net"
	"os"
)

// These pin the phase 0 contract (spec 2.12): other streams call these
// while stream P writes them, so changing one needs the lead, and must
// change this file too.
var (
	_ func(StartOptions) (*Proc, error)                    = Start
	_ func(string, int) (bool, int)                        = PortInUse
	_ func(string) (*os.File, error)                       = OpenShared
	_ func() (net.IP, error)                               = DefaultGateway
	_ func(string) string                                  = KnownFolder
	_ func(context.Context, string, []string) (int, error) = RunElevatedWait
	_ func(int) string                                     = ProcName
	_ func(GameRule) bool                                  = GameFirewallReady
	_ func(string, string, bool, []GameRule) error         = FixFirewallAll
)
