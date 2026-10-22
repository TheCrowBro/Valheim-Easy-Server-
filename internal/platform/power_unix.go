//go:build !windows

package platform

import "time"

// HibernateStatus reports whether the computer can hibernate.
func HibernateStatus() (bool, string) {
	return false, "Hibernation is only available on Windows."
}

// Hibernate is only implemented on Windows.
func Hibernate() error { return ErrUnsupported }

// EnableHibernate is only implemented on Windows.
func EnableHibernate() error { return ErrUnsupported }

// KeepAwake is a no-op outside Windows.
type KeepAwake struct{}

// NewKeepAwake returns a no-op KeepAwake.
func NewKeepAwake(string) *KeepAwake { return &KeepAwake{} }

// Set does nothing outside Windows.
func (*KeepAwake) Set(bool) {}

// IdleTime is unknown outside Windows.
func IdleTime() (time.Duration, bool) { return 0, false }
