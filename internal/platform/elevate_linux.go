//go:build !windows && !darwin

package platform

import "context"

// RunElevatedWait runs exe as an administrator and waits for it. Linux
// only runs the tests, which never need it, so it isn't done here.
func RunElevatedWait(ctx context.Context, exe string, args []string) (int, error) {
	return -1, errNotHere
}
