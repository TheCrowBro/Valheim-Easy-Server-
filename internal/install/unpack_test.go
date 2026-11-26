package install

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeArchive(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// runnable checks a file's exec bits; Windows has none to check.
func runnable(t *testing.T, p string, want bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); (got&0o111 != 0) != want || got&0o200 == 0 {
		t.Errorf("%s has mode %v, runnable want %v (and always owner-writable)", filepath.Base(p), got, want)
	}
}

// unpackDir is a fresh folder inside a parent the test can check stays
// empty apart from it, so nothing lands beside or above it.
func unpackDir(t *testing.T) (parent, dir string) {
	parent = t.TempDir()
	return parent, filepath.Join(parent, "out")
}

func onlyOut(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "out" {
			t.Errorf("something was written beside the folder: %s", e.Name())
		}
	}
}

func TestUnzip(t *testing.T) {
	zipPath := writeArchive(t, "server.zip", makeZip(t, []entry{
		{name: "1461/", mode: fs.ModeDir | 0o755},
		{name: "1461/Windows/TerrariaServer.exe", body: "exe"},                                    // no Unix modes: Windows zip
		{name: "1461/Mac/Terraria Server.app/Contents/MacOS/TerrariaServer.bin.osx", body: "mac"}, // runnable only through exec
		{name: "1461/Linux/TerrariaServer.bin.x86_64", body: "linux", mode: 0o755},                // a Unix zip's own bit
		{name: "1461/Linux/readme.txt", body: "read me", mode: 0o444},                             // read-only becomes writable
		{name: `1461\Windows\backslash.txt`, body: "made on Windows"},
		{name: "__MACOSX/1461/._TerrariaServer.exe", body: "resource fork"},
		{name: "1461/Mac/osx/libFNA3D.dylib", body: "lib"},
		{name: "1461/Mac/osx/libFNA3D.0.dylib", body: "libFNA3D.dylib", mode: fs.ModeSymlink | 0o777},
	}))
	parent, dir := unpackDir(t)
	var asked []string
	err := Unzip(zipPath, dir, func(name string) bool {
		asked = append(asked, name)
		return strings.HasSuffix(name, ".bin.osx")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "1461", "Windows", "TerrariaServer.exe")); got != "exe" {
		t.Errorf("TerrariaServer.exe = %q", got)
	}
	mac := filepath.Join(dir, "1461", "Mac", "Terraria Server.app", "Contents", "MacOS", "TerrariaServer.bin.osx")
	runnable(t, mac, true)
	runnable(t, filepath.Join(dir, "1461", "Linux", "TerrariaServer.bin.x86_64"), true)
	runnable(t, filepath.Join(dir, "1461", "Windows", "TerrariaServer.exe"), false)
	runnable(t, filepath.Join(dir, "1461", "Linux", "readme.txt"), false)
	if got := readFile(t, filepath.Join(dir, "1461", "Windows", "backslash.txt")); got != "made on Windows" {
		t.Errorf("backslash.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "__MACOSX")); !os.IsNotExist(err) {
		t.Error("a Mac's resource files were unpacked")
	}
	// The link works (as a link, or as a copy where links can't be made).
	if got := readFile(t, filepath.Join(dir, "1461", "Mac", "osx", "libFNA3D.0.dylib")); got != "lib" {
		t.Errorf("the link reads %q", got)
	}
	if !strings.Contains(strings.Join(asked, "\n"), "1461/Mac/Terraria Server.app/Contents/MacOS/TerrariaServer.bin.osx") {
		t.Errorf("exec was asked about %q", asked)
	}
	onlyOut(t, parent)
}

func TestUntar(t *testing.T) {
	tgz := writeArchive(t, "dotnet.tar.gz", makeTarGz(t, []entry{
		{name: "./", mode: fs.ModeDir},
		{name: "./dotnet", body: "host", mode: 0o755},
		{name: "./shared/Microsoft.NETCore.App/10.0.15/System.Private.CoreLib.dll", body: "corelib", mode: 0o444},
		{name: "./shared/Microsoft.NETCore.App/10.0.15/createdump", body: "dump", mode: 0o755},
		{name: "jdk/legal/java.xml/LICENSE", body: "../java.base/LICENSE", mode: fs.ModeSymlink | 0o777},
		{name: "jdk/legal/java.base/LICENSE", body: "GPL"},
		{name: "jdk/lib/libjli.dylib", body: "jli"},
		{name: "jdk/MacOS/libjli.dylib", hard: "jdk/lib/libjli.dylib"},
	}))
	parent, dir := unpackDir(t)
	if err := Untar(tgz, dir); err != nil {
		t.Fatal(err)
	}
	runnable(t, filepath.Join(dir, "dotnet"), true)
	runnable(t, filepath.Join(dir, "shared", "Microsoft.NETCore.App", "10.0.15", "createdump"), true)
	runnable(t, filepath.Join(dir, "shared", "Microsoft.NETCore.App", "10.0.15", "System.Private.CoreLib.dll"), false)
	if got := readFile(t, filepath.Join(dir, "jdk", "legal", "java.xml", "LICENSE")); got != "GPL" {
		t.Errorf("the symbolic link reads %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "jdk", "MacOS", "libjli.dylib")); got != "jli" {
		t.Errorf("the hard link reads %q", got)
	}
	if runtime.GOOS != "windows" {
		if target, err := os.Readlink(filepath.Join(dir, "jdk", "legal", "java.xml", "LICENSE")); err != nil || target != "../java.base/LICENSE" {
			t.Errorf("the link is %q, %v", target, err)
		}
	}
	onlyOut(t, parent)
}

// Names and links that could reach outside the folder fail the whole
// unpack and write nothing outside it.
func TestUnpackRefusesUnsafePaths(t *testing.T) {
	names := []string{
		"../evil.txt",
		"/etc/evil.txt",
		"C:/Windows/evil.txt",
		"C:evil.txt",
		"server/../../evil.txt",
		"server/../evil.txt", // inside, but nobody needs it: refused like the rest
		`..\evil.txt`,
		`server\..\..\evil.txt`,
		"server/file.txt:stream",
		"",
	}
	if runtime.GOOS == "windows" {
		names = append(names, "server/NUL", "COM1")
	}
	for _, name := range names {
		files := []entry{{name: "server/ok.txt", body: "ok"}, {name: name, body: "evil"}}
		_, zdir := unpackDir(t)
		if err := Unzip(writeArchive(t, "x.zip", makeZip(t, files)), zdir, nil); !errors.Is(err, errUnsafe) {
			t.Errorf("Unzip with %q = %v", name, err)
		}
		_, tdir := unpackDir(t)
		if err := Untar(writeArchive(t, "x.tar.gz", makeTarGz(t, files)), tdir); !errors.Is(err, errUnsafe) {
			t.Errorf("Untar with %q = %v", name, err)
		}
	}

	link := func(name, target string) entry { return entry{name: name, body: target, mode: fs.ModeSymlink | 0o777} }
	both := func(files []entry) (zerr, terr error) {
		zparent, zdir := unpackDir(t)
		zerr = Unzip(writeArchive(t, "x.zip", makeZip(t, files)), zdir, nil)
		onlyOut(t, zparent)
		tparent, tdir := unpackDir(t)
		terr = Untar(writeArchive(t, "x.tar.gz", makeTarGz(t, files)), tdir)
		onlyOut(t, tparent)
		return zerr, terr
	}
	for _, l := range []entry{
		link("lib/escape", "../../outside"),
		link("lib/absolute", "/etc/passwd"),
		link("lib/drive", "C:/Windows/System32"),
		link("lib/updown", "x/../../.."),  // ".." after going down
		link("self", "."),                 // the folder itself
		link("lib/back", `..\..\outside`), // backslashes too
	} {
		zerr, terr := both([]entry{{name: "lib/ok.txt", body: "ok"}, l})
		if !errors.Is(zerr, errUnsafe) || !errors.Is(terr, errUnsafe) {
			t.Errorf("link %q -> %q: Unzip = %v, Untar = %v", l.name, l.body, zerr, terr)
		}
	}

	// A chain: deep/er/a -> .. is deep, inside. A link made at deep/er/a/b
	// would really be made in deep, where ../../x is outside, though it
	// reads as inside from its name. Links are never made through links.
	if err := os.Symlink("x", filepath.Join(t.TempDir(), "probe")); err != nil {
		t.Logf("skipping the chain check: this system won't make links (%v)", err)
		return
	}
	if zerr, terr := both([]entry{link("deep/er/a", ".."), {name: "deep/ok.txt", body: "ok"}}); zerr != nil || terr != nil {
		t.Errorf("a link to a folder inside was refused: %v / %v", zerr, terr)
	}
	zerr, terr := both([]entry{link("deep/er/a", ".."), link("deep/er/a/b", "../../x")})
	if !errors.Is(zerr, errUnsafe) || !errors.Is(terr, errUnsafe) {
		t.Errorf("a link made through a link: Unzip = %v, Untar = %v", zerr, terr)
	}

	// A hard link to a name outside.
	_, tdir := unpackDir(t)
	err := Untar(writeArchive(t, "x.tar.gz", makeTarGz(t, []entry{{name: "a", hard: "../../etc/passwd"}})), tdir)
	if !errors.Is(err, errUnsafe) {
		t.Errorf("hard link outside: %v", err)
	}
}

// A link already in the folder (from before) can't carry a file outside:
// writes go through an os.Root on the folder.
func TestUnpackThroughOldLink(t *testing.T) {
	parent, dir := unpackDir(t)
	outside := filepath.Join(parent, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "lib")); err != nil {
		t.Skipf("this system won't make a link for the test: %v", err)
	}
	files := []entry{{name: "lib/evil.txt", body: "evil"}}
	if err := Untar(writeArchive(t, "x.tar.gz", makeTarGz(t, files)), dir); err == nil {
		t.Error("Untar wrote through a link to outside")
	}
	if err := Unzip(writeArchive(t, "x.zip", makeZip(t, files)), dir, nil); err == nil {
		t.Error("Unzip wrote through a link to outside")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("files were written outside: %v", entries)
	}
}

func TestUnpackDamaged(t *testing.T) {
	bad := writeArchive(t, "bad.zip", []byte("not an archive"))
	if err := Unzip(bad, t.TempDir(), nil); err == nil {
		t.Error("Unzip of a damaged file worked")
	}
	if err := Untar(bad, t.TempDir()); err == nil {
		t.Error("Untar of a damaged file worked")
	}
}
