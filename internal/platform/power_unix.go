//go:build !windows && !darwin

package platform

import "time"

// HibernateStatus reports whether the computer can hibernate.
func HibernateStatus() (bool, string) {
	return false, "Hibernation is only available on Windows."
}

// Hibernate is only implemented on Windows.
func Hibernate() error { return ErrUnsupported }

// SleepStatus reports whether the computer can sleep.
func SleepStatus() (bool, string) {
	return false, "Sleep is only available on Windows."
}

// Sleep is only implemented on Windows.
func Sleep() error { return ErrUnsupported }

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

// AfterWake has nothing to do here.
func AfterWake() {}
