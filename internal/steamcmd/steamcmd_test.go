//go:build !windows

package steamcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSteamCMD sets up a steamcmd.sh that, like Valve's, runs the real
// SteamCMD (here the shell script child) as a child rather than in its
// place.
func fakeSteamCMD(t *testing.T, child string) *SteamCMD {
	t.Helper()
	dir := t.TempDir()
	wrapper := "#!/bin/sh\n\"$(dirname \"$0\")/child.sh\" \"$@\"\nexit $?\n"
	if err := os.WriteFile(filepath.Join(dir, "steamcmd.sh"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child.sh"), []byte("#!/bin/sh\n"+child), 0o755); err != nil {
		t.Fatal(err)
	}
	return &SteamCMD{Dir: dir, InstallDir: filepath.Join(dir, "server")}
}

// Cancelling an install (or quitting the app during one) stops SteamCMD
// straight away, not when its download is done. The pretend SteamCMD, like
// many network programs, carries on when nothing reads its output any more.
func TestCancelStopsSteamCMD(t *testing.T) {
	s := fakeSteamCMD(t, `trap '' PIPE
i=0
while [ $i -lt 100 ]; do
  echo tick >> "$(dirname "$0")/ticks"
  echo " Update state (0x61) downloading, progress: $i.00 (100 / 10000)"
  i=$((i+1))
  sleep 0.1
done
echo "Success! App '896660' fully installed."
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	downloading := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		done <- s.Install(ctx, false, func(p Progress) {
			if p.Percent >= 0 {
				once.Do(func() { close(downloading) })
			}
		})
	}()
	select {
	case <-downloading:
	case <-time.After(10 * time.Second):
		t.Fatal("the pretend download never started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "cancelled" {
			t.Fatalf("Install = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Install carried on after the cancel")
	}

	// The real SteamCMD has stopped too, not only steamcmd.sh.
	ticks := filepath.Join(s.Dir, "ticks")
	size := func() int64 {
		st, err := os.Stat(ticks)
		if err != nil {
			t.Fatal(err)
		}
		return st.Size()
	}
	time.Sleep(300 * time.Millisecond)
	before := size()
	time.Sleep(time.Second)
	if size() != before {
		t.Fatal("SteamCMD carried on downloading after the cancel")
	}
}

// On a Mac with Apple silicon and no Rosetta, the shell says it can't run
// SteamCMD; the app says how to fix that.
func TestBadCPUTypeExplainsRosetta(t *testing.T) {
	s := fakeSteamCMD(t, `echo "$(dirname "$0")/steamcmd.sh: line 37: $(dirname "$0")/steamcmd: Bad CPU type in executable" >&2
exit 126
`)
	err := s.Install(context.Background(), false, func(Progress) {})
	if err == nil || !strings.Contains(err.Error(), "softwareupdate --install-rosetta") {
		t.Fatalf("Install = %v", err)
	}
}
