package install

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"valheim-easy-server/internal/game"
)

var payload = strings.Repeat("server files ", 40000) // about 500 kB

func sums(b []byte) map[string]string {
	m5 := md5.Sum(b)
	s1 := sha1.Sum(b)
	s256 := sha256.Sum256(b)
	s512 := sha512.Sum512(b)
	return map[string]string{
		"md5":    hex.EncodeToString(m5[:]),
		"sha1":   hex.EncodeToString(s1[:]),
		"sha256": hex.EncodeToString(s256[:]),
		"sha512": hex.EncodeToString(s512[:]),
	}
}

func payloadServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/server.zip" {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "server.zip", time.Time{}, strings.NewReader(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// onlyDest checks that a download left nothing in dir but what it should.
func onlyDest(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the folder has %q, want %q", got, want)
	}
}

// Every checksum a source publishes is checked: the right one is accepted
// (in either letter case), a wrong one fails and leaves nothing behind,
// not even over an older file of the same name.
func TestDownloadChecksums(t *testing.T) {
	srv := payloadServer(t)
	right := sums([]byte(payload))
	wrong := sums([]byte("something else"))
	for _, algo := range []string{"md5", "sha1", "sha256", "sha512"} {
		t.Run(algo, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "server.zip")
			if err := Download(context.Background(), srv.URL+"/server.zip", Hash{Algo: algo, Hex: strings.ToUpper(right[algo])}, dest, nil); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(dest); string(b) != payload {
				t.Fatal("the downloaded file isn't what was served")
			}

			if err := os.WriteFile(dest, []byte("the older build"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := Download(context.Background(), srv.URL+"/server.zip", Hash{Algo: algo, Hex: wrong[algo]}, dest, nil)
			if !errors.Is(err, errChecksum) {
				t.Fatalf("Download with a wrong %s = %v", algo, err)
			}
			if !strings.Contains(err.Error(), "The download of server.zip was damaged: its "+algo+" checksum doesn't match") {
				t.Errorf("error text = %q", err)
			}
			if b, _ := os.ReadFile(dest); string(b) != "the older build" {
				t.Error("a damaged download replaced the file that was there")
			}
			onlyDest(t, dir, "server.zip")
		})
	}
}

func TestDownloadHashProblems(t *testing.T) {
	srv := payloadServer(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "server.zip")
	for _, h := range []Hash{
		{Algo: "crc32", Hex: "00000000"},
		{Algo: "sha256", Hex: "not hex"},
		{Algo: "sha1", Hex: sums([]byte(payload))["md5"]}, // the wrong length for sha1
	} {
		if err := Download(context.Background(), srv.URL+"/server.zip", h, dest, nil); err == nil {
			t.Errorf("Download with %+v worked", h)
		}
	}
	onlyDest(t, dir)
	// No checksum published (Terraria): the file is taken as it comes.
	if err := Download(context.Background(), srv.URL+"/server.zip", Hash{}, dest, nil); err != nil {
		t.Fatal(err)
	}
	onlyDest(t, dir, "server.zip")
}

func TestDownloadProgressAndErrors(t *testing.T) {
	srv := payloadServer(t)
	dir := t.TempDir()
	var steps []game.Progress
	err := Download(context.Background(), srv.URL+"/server.zip", Hash{}, filepath.Join(dir, "sub", "server.zip"), func(p game.Progress) {
		steps = append(steps, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) < 3 || steps[0].Percent != 0 || steps[len(steps)-1].Percent != 100 {
		t.Fatalf("progress = %v", steps)
	}
	for i, p := range steps {
		if p.Step != "Downloading server.zip…" || (i > 0 && p.Percent <= steps[i-1].Percent) {
			t.Fatalf("progress = %v", steps)
		}
	}

	err = Download(context.Background(), srv.URL+"/missing.zip", Hash{}, filepath.Join(dir, "missing.zip"), nil)
	if err == nil || err.Error() != "Couldn't download missing.zip (the server said 404 Not Found)." {
		t.Errorf("404: %v", err)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	err = Download(context.Background(), closed.URL+"/server.zip", Hash{}, filepath.Join(dir, "server.zip"), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "Couldn't download server.zip. Check the internet connection.") {
		t.Errorf("no server: %v", err)
	}
}

// A slow server: some data, then nothing until the test lets it go on.
func slowServer(t *testing.T) (srv *httptest.Server, started <-chan struct{}) {
	t.Helper()
	ch := make(chan struct{})
	var once sync.Once
	release := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.Write([]byte(strings.Repeat("x", 1000)))
		w.(http.Flusher).Flush()
		once.Do(func() { close(ch) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv, ch
}

func TestDownloadCancel(t *testing.T) {
	srv, started := slowServer(t)
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Download(ctx, srv.URL+"/big.zip", Hash{}, filepath.Join(dir, "big.zip"), nil) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Download = %v, want a cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a cancelled download carried on")
	}
	onlyDest(t, dir)
}

// A download that stops receiving anything is given up, so an install
// never hangs on a dead connection.
func TestDownloadStall(t *testing.T) {
	old := stallTimeout
	stallTimeout = 300 * time.Millisecond
	defer func() { stallTimeout = old }()
	srv, _ := slowServer(t)
	dir := t.TempDir()
	start := time.Now()
	err := Download(context.Background(), srv.URL+"/big.zip", Hash{}, filepath.Join(dir, "big.zip"), nil)
	if err == nil || err.Error() != "The download of big.zip stopped making progress. Check the internet connection and try again." {
		t.Errorf("Download = %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the stalled download took %v to give up", took)
	}
	onlyDest(t, dir)
}
