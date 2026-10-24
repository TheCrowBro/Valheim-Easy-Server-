// Package modstest is a stand-in for Thunderstore (or Hexium), serving small
// copies of the mods' packages, laid out like the real ones.
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
	Loader    = "denikson-BepInExPack_Valheim"
	Jotunn    = "ValheimModding-Jotunn"
	Portal    = "RandyKnapp-AdvancedPortals"
	Yaml      = "ValheimModding-YamlDotNet"
	Seasons   = "RustyMods-Seasonality"
	Rivers    = "Gurebu-Riverheim"
	Plants    = "Advize-PlantEverything"
	Gamma     = "shudnal-GammaOfNightLights"
	Sync      = "shudnal-ConditionalConfigSync"
	Dungeons  = "hoskope-DarkerDungeons"
	Fuel      = "Marf-FuelEternal"
	Heightmap = "Digitalroot-Heightmap_Unlimited_JvL"
	Inventory = "Azumatt-AzuExtendedPlayerInventory" // on Hexium
)

// Fake answers the package and download requests mods.Installer makes.
type Fake struct {
	Requests atomic.Int32

	mu       sync.Mutex
	versions map[string]string
	deps     map[string][]string          // added with AddPackage
	extra    map[string]map[string]string // package -> extra file name -> content
}

// New returns a Fake Thunderstore offering the current versions of the
// packages.
func New() *Fake {
	return &Fake{
		versions: map[string]string{Loader: "5.4.2351", Jotunn: "2.30.2", Portal: "1.2.0", Yaml: "16.3.1", Seasons: "3.8.4",
			Rivers: "1.2.1", Plants: "1.21.4", Gamma: "1.0.10", Sync: "1.0.11", Dungeons: "1.0.1", Fuel: "1.2.1", Heightmap: "2.0.2"},
		deps:  map[string][]string{},
		extra: map[string]map[string]string{},
	}
}

// NewHexium returns a Fake Hexium, which has the mod authors who moved
// there and its own copy of BepInEx.
func NewHexium() *Fake {
	return &Fake{versions: map[string]string{Loader: "5.4.2351", Inventory: "2.6.4"}, deps: map[string][]string{}, extra: map[string]map[string]string{}}
}

// SetVersion makes a newer (or older) version of a package the latest one.
func (f *Fake) SetVersion(name, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[name] = version
}

// AddPackage offers another package, which needs deps.
func (f *Fake) AddPackage(name, version string, deps ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions[name] = version
	f.deps[name] = deps
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
	Loader:    nil,
	Jotunn:    {Loader + "-5.4.2333"},
	Portal:    {Loader + "-5.4.2333", Jotunn + "-2.27.0"},
	Yaml:      nil,
	Seasons:   {Loader + "-5.4.2333", Yaml + "-16.3.1"},
	Rivers:    {Loader + "-5.4.2202"},
	Plants:    {Loader + "-5.4.2351"},
	Gamma:     {Loader + "-5.4.2350", Sync + "-1.0.5"},
	Sync:      {Loader + "-5.4.2350"},
	Dungeons:  {Loader + "-5.4.2333"},
	Fuel:      {Loader + "-5.4.2102"},
	Heightmap: {Loader + "-5.4.2350", Jotunn + "-2.30.0"},
	Inventory: {Loader + "-5.4.2351"},
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
			"dependencies":   append(deps[name], f.deps[name]...),
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
	case Yaml:
		files["plugins/YamlDotNet.dll"] = "yaml " + version
		files["LICENSE.txt"] = "MIT"
	case Seasons:
		files["Seasonality.dll"] = "seasons " + version // at the top, like the real one
		files["config/Seasonality/Textures/winter.png"] = "snow"
	case Rivers:
		files["BepInEx/plugins/Riverheim.dll"] = "rivers " + version // laid out like the game folder
		files["CHANGELOG.md"] = "# Changes"
	case Plants:
		files["Advize_PlantEverything.dll"] = "plants " + version
	case Gamma:
		files["GammaOfNightLights.dll"] = "gamma " + version
	case Sync:
		files["ConditionalConfigSync.dll"] = "sync " + version
		files["ConditionalConfigSync.Plugin.dll"] = "sync plugin " + version
		files["ConditionalConfigSync.xml"] = "<doc/>"
	case Dungeons:
		files["DarkerDungeons.dll"] = "dungeons " + version
	case Fuel:
		files["FuelEternal.dll"] = "fuel " + version
	case Heightmap:
		files["plugins/Digitalroot.HeightmapUnlimitedJvL.dll"] = "heightmap " + version
		files["plugins/LICENSE"] = "AGPL"
	case Inventory:
		files["AzuExtendedPlayerInventory.dll"] = "inventory " + version
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
