//go:build !windows

package tray

// Run has no tray outside Windows; it just waits until done is closed.
func Run(o Options, done <-chan struct{}) { <-done }
