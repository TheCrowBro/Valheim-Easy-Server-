module valheim-easy-server

// Go 1.26 is the newest Go whose Mac programs start on macOS 12 Monterey,
// the last macOS for a 2013 Mac Pro. CI checks the Mac app still does.
go 1.26.10

require (
	fyne.io/systray v1.12.2
	golang.org/x/crypto v0.58.0
	golang.org/x/crypto/x509roots/fallback v0.0.0-20261030153154-30a33513ff6d
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.49.0
)

require github.com/godbus/dbus/v5 v5.1.0 // indirect
