// Package tray shows the app's icon in the Windows notification area, with
// a menu to open the control panel, start and stop the server, hibernate
// (or sleep), and close the app.
package tray

// Options connect the tray menu to the app.
type Options struct {
	Tooltip   func() string // short status, e.g. "Valheim server: Online, 3 playing"
	Running   func() bool
	RestLabel func() string // "Hibernate computer" or "Put computer to sleep"
	Open      func()
	Start     func()
	Stop      func()
	Hibernate func()
	Quit      func(stopServer bool)
	Changes   func(func()) // registers a callback for status changes
}
