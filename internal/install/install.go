// Package install has the generic helpers game adapters use to put a
// server on this computer: the shared SteamCMD, checked downloads,
// unpacking, the private Java and .NET runtimes, Windows prerequisites and
// the build file of non-Steam installs (spec 2.3).
//
// Each game's own version and download resolver lives in its adapter
// package, not here, so the stream that owns a game owns the facts it must
// check. Temurin (Java) and .NET are shared by several games, so their
// resolvers are here.
package install

import (
	"net/http"
)

// client makes every request this package sends. It has no overall time
// limit, because a big download on a slow line can take a long time;
// Download watches for a download that stops moving instead. Tests swap
// its transport for recorded answers, so no test touches the network.
var client = &http.Client{}

// userAgent is what the app calls itself to download sites, as the mods
// and update downloads already do.
const userAgent = "ValheimEasyServer"
