//go:build darwin

package platform

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// RunElevatedWait runs exe as an administrator and waits for it, returning
// its exit code. macOS asks for an administrator's password on the screen
// (osascript); this waits for that answer too. A cancelled ctx stops the
// wait (-1 and ctx's error).
func RunElevatedWait(ctx context.Context, exe string, args []string) (int, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", macElevatedScript(exe, args))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	if err != nil {
		if macUserCanceled(stderr.String()) {
			return -1, errors.New("the administrator password prompt was cancelled")
		}
		return -1, fmt.Errorf("macOS couldn't run %s as an administrator: %v %s", exe, err, strings.TrimSpace(stderr.String()))
	}
	code, ok := parseElevatedExit(string(out))
	if !ok {
		return -1, fmt.Errorf("couldn't read how %s ended: %q", exe, out)
	}
	return code, nil
}
