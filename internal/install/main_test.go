package install

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"valheim-easy-server/internal/steamcmd/steamcmdtest"
)

// TestMain lets the test binary also play SteamCMD (see steamcmdtest), so
// the SteamCMD tests run the real installer on every system.
func TestMain(m *testing.M) {
	steamcmdtest.Main(m)
}

// handlerTransport answers requests from a handler in the test, so tests
// that go through client never touch the network.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	resp := rec.Result()
	resp.Request = r
	return resp, nil
}

// answerFrom makes client answer from h for the rest of the test.
func answerFrom(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	old := client
	client = &http.Client{Transport: handlerTransport{h}}
	t.Cleanup(func() { client = old })
}

// entry is one file, folder or link in a test archive.
type entry struct {
	name string
	body string
	mode fs.FileMode // 0 = 0644; fs.ModeDir, fs.ModeSymlink (body = target)
	hard string      // tar only: a hard link to this archive name
}

func makeTarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: int64(e.mode.Perm()), Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		body := e.body
		switch {
		case e.hard != "":
			h.Typeflag, h.Linkname, h.Size, body = tar.TypeLink, e.hard, 0, ""
		case e.mode&fs.ModeDir != 0:
			h.Typeflag, h.Size, body = tar.TypeDir, 0, ""
			if e.mode.Perm() == 0 {
				h.Mode = 0o755
			}
		case e.mode&fs.ModeSymlink != 0:
			h.Typeflag, h.Linkname, h.Size, body = tar.TypeSymlink, e.body, 0, ""
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// makeZip makes a zip. Entries with a mode get Unix modes, as a zip made on
// a Mac or Linux has; entries without one have none, as Windows' zips.
func makeZip(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if e.mode&fs.ModeDir == 0 {
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
