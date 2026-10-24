package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test program stand in for an updated copy of the app.
func TestMain(m *testing.M) {
	switch os.Getenv("VES_TEST_APP") {
	case "":
		os.Exit(m.Run())
	case "starts": // opens its control panel, like the app
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(2)
		}
		b, _ := json.Marshal(instanceInfo{Port: ln.Addr().(*net.TCPAddr).Port})
		os.WriteFile(filepath.Join(os.Getenv("VES_TEST_DATA"), ".instance.json"), b, 0o644)
		http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "{}")
		}))
	case "closes":
		os.Exit(3)
	case "hangs":
		time.Sleep(time.Minute)
	}
}

func TestRelaunchArgs(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want []string
	}{
		{nil, []string{"--background", "--port=8456"}},
		{[]string{"--data", `C:\Valheim`}, []string{"--data", `C:\Valheim`, "--background", "--port=8456"}},
		{[]string{"--background", "--port=8456"}, []string{"--background", "--port=8456"}},
		{[]string{"-no-window", "--port", "9000"}, []string{"-no-window", "--port=8456"}},
		{[]string{"--data", "port", "-background=false"}, []string{"--data", "port", "-background=false", "--background", "--port=8456"}},
	} {
		if got := relaunchArgs(c.in, 8456); !reflect.DeepEqual(got, c.want) {
			t.Errorf("relaunchArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWaitStarted(t *testing.T) {
	for _, c := range []struct {
		mode string
		ok   bool
	}{{"starts", true}, {"closes", false}, {"hangs", false}} {
		dir := t.TempDir()
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "VES_TEST_APP="+c.mode, "VES_TEST_DATA="+dir)
		err := waitStarted(cmd, dir, 3*time.Second)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.mode, err)
		}
		if c.ok {
			cmd.Process.Kill()
		}
	}
}

func TestStartUpdatedGoesBack(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "app.exe")
	for _, path := range []string{exe, exe + ".old"} {
		copyFile(t, os.Args[0], path)
	}
	t.Setenv("VES_TEST_APP", "closes") // the update, and then the previous version too
	var logs strings.Builder
	startUpdated(exe, nil, dir, log.New(&logs, "", 0))
	if _, err := os.Stat(exe + ".old"); err == nil || !strings.Contains(logs.String(), "going back to the previous version") {
		t.Fatalf("the previous version wasn't put back:\n%s", logs.String())
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	dst.Close()
}
