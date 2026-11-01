//go:build !windows

package platform

import (
	"fmt"
	"testing"
	"time"
)

func TestSingleInstance(t *testing.T) {
	id := fmt.Sprintf("test-%d", time.Now().UnixNano())
	release, already, err := SingleInstance(id)
	if err != nil || already {
		t.Fatalf("first: %v %v", already, err)
	}
	if _, already, err := SingleInstance(id); err != nil || !already {
		t.Fatalf("second: %v %v", already, err)
	}
	release()
	release2, already, err := SingleInstance(id)
	if err != nil || already {
		t.Fatalf("after release: %v %v", already, err)
	}
	release2()
}
