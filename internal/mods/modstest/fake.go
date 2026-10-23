// Package modstest is a stand-in for Thunderstore, serving small copies of
// the packages Advanced Portals needs, laid out like the real ones.
package modstest

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	Loader = "denikson-BepInExPack_Valheim"
	Jotunn = "ValheimModding-Jotunn"
	Portal = "RandyKnapp-AdvancedPortals"
)

// Fake answers the package and download requests mods.Installer makes.
type Fake struct {
	Requests atomic.Int32

	mu       sync.Mutex
	versions map[string]string
	extra    map[string]map[string]string // package -> extra file name -> content
}

// New returns a Fake offering the current versions of the three packages.
func New() *Fake {
	return &Fake{
		versions: map[string]string{Loader: "5.4.2351", Jotunn: "2.30.2", Portal: "1.2.0"},
		extra:    map[string]map[string]string{},
	}
}

// SetVersion makes a newer (or older) version of a package the latest one.
func (f *Fake) SetVersion(name, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[name] = version
}

// AddFile puts an extra file into a package's zip.
func (f *Fake) AddFile(name, file, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.extra[name] == nil {
		f.extra[name] = map[string]string{}
	}
	f.extra[name][file] = content
}

var deps = map[string][]string{
	Loader: nil,
	Jotunn: {Loader + "-5.4.2333"},
	Portal: {Loader + "-5.4.2333", Jotunn + "-2.27.0"},
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Requests.Add(1)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case len(parts) == 5 && parts[0] == "api" && parts[2] == "package":
		name := parts[3] + "-" + parts[4]
		v, ok := f.versions[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"latest": map[string]any{
			"version_number": v,
			"download_url":   fmt.Sprintf("http://%s/package/download/%s/%s/%s/", r.Host, parts[3], parts[4], v),
			"dependencies":   deps[name],
		}})
	case len(parts) == 5 && parts[0] == "package" && parts[1] == "download":
		name := parts[2] + "-" + parts[3]
		w.Header().Set("Content-Type", "application/zip")
		w.Write(f.zip(name, parts[4]))
	default:
		http.NotFound(w, r)
	}
}

// zip builds a package laid out the way its real zip is.
func (f *Fake) zip(name, version string) []byte {
	files := map[string]string{"manifest.json": `{"name":"` + name + `","version_number":"` + version + `"}`, "README.md": "# " + name}
	switch name {
	case Loader:
		files["BepInExPack_Valheim/winhttp.dll"] = "doorstop " + version
		files["BepInExPack_Valheim/doorstop_config.ini"] = "[General]\r\nenabled = true\r\n"
		files["BepInExPack_Valheim/BepInEx/core/BepInEx.Preloader.dll"] = "preloader " + version
		files["BepInExPack_Valheim/BepInEx/config/BepInEx.cfg"] = "[Logging.Console]\r\n\r\n## Enables showing a console for log output.\r\nEnabled = true\r\n\r\nPreventClose = true\r\n\r\n[Logging.Disk]\r\n\r\nEnabled = true\r\n"
	case Jotunn:
		files[`plugins\Jotunn.dll`] = "jotunn " + version
	case Portal:
		files["plugins/AdvancedPortals.dll"] = "portals " + version
		files["icon.png"] = "png"
	}
	for k, v := range f.extra[name] {
		files[k] = v
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for file, content := range files {
		w, _ := zw.Create(file)
		w.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}
