//go:build windows

package zomboid

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// longPath spells a folder's short "8.3" names out in full, and leaves a
// path Windows can't look up (it doesn't exist yet) as it is.
func TestLongPath(t *testing.T) {
	long := filepath.Join(t.TempDir(), "A folder with a long name")
	if err := os.Mkdir(long, 0o755); err != nil {
		t.Fatal(err)
	}
	long = longPath(long) // TEMP itself may be a short name (GitHub's runners)
	if strings.Contains(long, "~") {
		t.Errorf("longPath left a short name in %q", long)
	}
	p, _ := syscall.UTF16PtrFromString(long)
	buf := make([]uint16, 1024)
	n, err := syscall.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Skipf("Windows gives no short name here: %v", err)
	}
	short := syscall.UTF16ToString(buf[:n])
	t.Logf("short name %q", short)
	if got := longPath(short); !strings.EqualFold(got, long) {
		t.Errorf("longPath(%q) = %q, want %q", short, got, long)
	}
	missing := filepath.Join(short, "not there yet")
	if got := longPath(missing); got != missing {
		t.Errorf("longPath of a missing folder = %q, want it as it was", got)
	}
}
