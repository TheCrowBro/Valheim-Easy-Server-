package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcName(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ProcName(os.Getpid()), filepath.Base(exe); !strings.EqualFold(got, want) {
		t.Errorf("ProcName(this test) = %q, want %q", got, want)
	}
	// No such process: Windows' IDs are multiples of 4, and the others
	// stay far below this.
	for _, pid := range []int{0, -1, 1<<30 + 3} {
		if got := ProcName(pid); got != "" {
			t.Errorf("ProcName(%d) = %q", pid, got)
		}
	}
}
