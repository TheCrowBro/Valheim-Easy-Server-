package platform

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

func TestMacElevatedScript(t *testing.T) {
	got := macElevatedScript(`/Users/me/Valheim Server/it's "here"/install`, []string{"--quiet", "a b"})
	want := `do shell script "'/Users/me/Valheim Server/it'\\''s \"here\"/install' '--quiet' 'a b' >/dev/null 2>&1; echo $?"` +
		` with administrator privileges with prompt "Setting up a game server needs an administrator's permission."`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	for out, want := range map[string]int{"0\n": 0, "7\r\n": 7, " 255 ": 255} {
		if code, ok := parseElevatedExit(out); !ok || code != want {
			t.Errorf("%q = %d %v", out, code, ok)
		}
	}
	if _, ok := parseElevatedExit(""); ok {
		t.Error("read an exit code from nothing")
	}
	if !macUserCanceled("0:73: execution error: User canceled. (-128)\n") || macUserCanceled("execution error: (-60007)") {
		t.Error("Cancel isn't told from other failures")
	}
}

func TestRunElevatedWaitLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux only")
	}
	code, err := RunElevatedWait(context.Background(), "/bin/true", nil)
	if code != -1 || !errors.Is(err, ErrUnsupported) || !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("got %d, %v", code, err)
	}
}
