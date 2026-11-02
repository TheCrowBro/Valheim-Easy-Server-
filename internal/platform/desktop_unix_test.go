//go:build !windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSingleInstance(t *testing.T) {
	dir := t.TempDir()
	id := fmt.Sprintf("test-%d", time.Now().UnixNano())
	release, already, err := SingleInstance(dir, id)
	if err != nil || already {
		t.Fatalf("first: %v %v", already, err)
	}
	if _, already, err := SingleInstance(dir, id); err != nil || !already {
		t.Fatalf("second: %v %v", already, err)
	}
	// The lock is in the data folder, where the system's clean-up of old
	// temp files can't take it away from the running copy.
	if _, err := os.Stat(filepath.Join(dir, ".instance.lock")); err != nil {
		t.Fatalf("no lock in the data folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.TempDir(), "ValheimEasyServer-"+id+".lock")); err == nil {
		t.Fatal("the lock went in the temp folder")
	}
	if _, already, err := SingleInstance(dir, id+"-other"); err != nil || !already {
		t.Fatalf("same data folder, other id: %v %v", already, err)
	}
	other, already, err := SingleInstance(t.TempDir(), id)
	if err != nil || already {
		t.Fatalf("another data folder: %v %v", already, err)
	}
	other()
	release()
	release2, already, err := SingleInstance(dir, id)
	if err != nil || already {
		t.Fatalf("after release: %v %v", already, err)
	}
	release2()
}
