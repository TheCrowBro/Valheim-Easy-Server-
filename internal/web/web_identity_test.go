package web

// The control panel's header (docs/dev/multigame/spec.md 1.2). A panel
// page that's open while the app updates keeps working with the new copy,
// and the update and rollback tests in CI send the header to whichever copy
// runs, so its name never changes. It's compared with its literal:
// renaming it fails here.

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdentity(t *testing.T) {
	if panelHeader != "X-VES" {
		t.Errorf("the panel's header = %q, want X-VES", panelHeader)
	}
	// A change asked for with the header gets through; without it, it's
	// refused.
	passed := false
	h := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { passed = true }))
	for _, header := range []string{"X-VES", ""} {
		passed = false
		req := httptest.NewRequest("POST", "/api/server/stop", nil)
		req.RemoteAddr, req.Host = "127.0.0.1:50000", "127.0.0.1:8456"
		if header != "" {
			req.Header.Set(header, "1")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if passed != (header != "") {
			t.Errorf("a change with header %q: got through %v, status %d", header, passed, rec.Code)
		}
	}
	// And the panel sends it.
	sends := false
	fs.WalkDir(uiFiles, "ui", func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".js") {
			b, _ := uiFiles.ReadFile(path)
			sends = sends || strings.Contains(string(b), "'X-VES'")
		}
		return nil
	})
	if !sends {
		t.Error("no script in the control panel sends the X-VES header")
	}
}
