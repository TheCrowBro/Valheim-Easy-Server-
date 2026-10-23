//go:build windows

package tray

import (
	_ "embed"
	"sync"

	"fyne.io/systray"
)

//go:embed icon.ico
var iconICO []byte

// Run shows the tray icon and runs its message loop. Call it from the main
// goroutine: the systray package locks it to the main OS thread, which is
// the thread that must create the tray window and receive its messages.
func Run(o Options, done <-chan struct{}) {
	systray.Run(func() { ready(o, done) }, func() {})
}

func ready(o Options, done <-chan struct{}) {
	systray.SetIcon(iconICO)
	systray.SetTitle("Valheim Easy Server")
	systray.SetTooltip("Valheim Easy Server")
	systray.SetOnTapped(func() { go o.Open() }) // left click opens the control panel

	open := systray.AddMenuItem("Open control panel", "")
	systray.AddSeparator()
	start := systray.AddMenuItem("Start server", "")
	stop := systray.AddMenuItem("Stop server", "Saves the world, then stops")
	systray.AddSeparator()
	hibernate := systray.AddMenuItem(o.RestLabel(), "Saves the world and stops the server first")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Close app (server keeps running)", "")
	quitStop := systray.AddMenuItem("Stop server and close app", "")

	var mu sync.Mutex
	refresh := func() {
		mu.Lock()
		defer mu.Unlock()
		tip := o.Tooltip()
		if len(tip) > 120 { // Windows limits tooltips to 127 characters
			tip = tip[:120]
		}
		systray.SetTooltip(tip)
		hibernate.SetTitle(o.RestLabel())
		if o.Running() {
			start.Disable()
			stop.Enable()
			quitStop.Show()
		} else {
			start.Enable()
			stop.Disable()
			quitStop.Hide()
		}
	}
	refresh()
	o.Changes(refresh)

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				o.Open()
			case <-start.ClickedCh:
				o.Start()
			case <-stop.ClickedCh:
				o.Stop()
			case <-hibernate.ClickedCh:
				o.Hibernate()
			case <-quit.ClickedCh:
				o.Quit(false)
			case <-quitStop.ClickedCh:
				o.Quit(true)
			case <-done:
				systray.Quit()
				return
			}
		}
	}()
}
