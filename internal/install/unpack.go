package install

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Unpacking rules, the same for zips and tarballs:
//
//   - Every file lands inside dir. A name that is absolute, has a drive or
//     a colon, a ".." anywhere, or (on Windows) is a device name such as NUL
//     is refused, and the whole unpack fails: a server download that tries
//     that is not one to run.
//   - Files are written through an os.Root on dir, so nothing can be written
//     through a link to outside it, even one that was already there.
//   - Files are made 0644, or 0755 when the archive marks them runnable (or
//     exec says so, for zips made on Windows, which carry no such marks).
//     Owner-writable always, so a later update or removal can replace them.
//   - Links are made last, once every file is in place, and only when they
//     stay inside dir (see linkProblem). Where the system won't make a
//     symbolic link (Windows without the right), the file it points to is
//     copied instead.
//   - Special files (devices, pipes) are skipped.

var errUnsafe = errors.New("unsafe path in archive")

// entryName checks one name from an archive. It returns the name cleaned,
// slash-separated and relative to dir, or "" for the archive's top folder
// itself ("./").
func entryName(name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/") // zips made on Windows sometimes use backslashes
	unsafe := name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, ":\x00")
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			unsafe = true
		}
	}
	clean := path.Clean(name)
	if clean == "." && !unsafe {
		return "", nil
	}
	if unsafe || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("the download has a file with an unsafe name (%q): %w", name, errUnsafe)
	}
	return clean, nil
}

// linkProblem checks a symbolic link at name (already cleaned) pointing at
// target. A target may climb with leading ".." and then only go down, and
// must stay inside dir. Together with linking only from real folders (see
// makeLinks), following any chain of such links never leaves dir.
func linkProblem(name, target string) error {
	t := strings.ReplaceAll(target, `\`, "/")
	bad := t == "" || strings.HasPrefix(t, "/") || strings.ContainsAny(t, ":\x00")
	climbing := true
	for _, part := range strings.Split(t, "/") {
		switch {
		case part == "..":
			if !climbing {
				bad = true
			}
		case part == "." || part == "":
		default:
			climbing = false
		}
	}
	resolved := path.Join(path.Dir(name), t)
	if bad || resolved == "." || !filepath.IsLocal(filepath.FromSlash(resolved)) {
		return fmt.Errorf("the download has a link that points outside its folder (%q -> %q): %w", name, target, errUnsafe)
	}
	return nil
}

// link is a symbolic or hard link waiting for the files to be in place.
type link struct {
	name   string // where the link goes
	target string // symbolic: as stored; hard: the archive name of the file
	hard   bool
}

// unpacker writes one archive's entries into dir.
type unpacker struct {
	root  *os.Root
	links []link
}

func newUnpacker(dir string) (*unpacker, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &unpacker{root: root}, nil
}

func (u *unpacker) close() { u.root.Close() }

func (u *unpacker) dir(name string) error {
	return u.root.MkdirAll(filepath.FromSlash(name), 0o755)
}

func (u *unpacker) file(name string, r io.Reader, runnable bool) error {
	rel := filepath.FromSlash(name)
	if parent := filepath.Dir(rel); parent != "." {
		if err := u.root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	perm := os.FileMode(0o644)
	if runnable {
		perm = 0o755
	}
	f, err := u.root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The file may have been there already with other bits, and new files
	// get the umask taken off: set the bits outright.
	return u.root.Chmod(rel, perm)
}

// makeLinks makes the links the archive asked for, after its files.
func (u *unpacker) makeLinks() error {
	for _, l := range u.links {
		rel := filepath.FromSlash(l.name)
		// A link is only made in a real folder, never through another link,
		// so where it points is exactly what linkProblem checked.
		for p := filepath.Dir(rel); p != "."; p = filepath.Dir(p) {
			if fi, err := u.root.Lstat(p); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
				return fmt.Errorf("the download has a link inside a link (%q): %w", l.name, errUnsafe)
			}
		}
		if parent := filepath.Dir(rel); parent != "." {
			if err := u.root.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}
		u.root.Remove(rel) // an earlier entry of the same name
		var err error
		if l.hard {
			err = u.root.Link(filepath.FromSlash(l.target), rel)
			if err != nil {
				err = u.copyFile(l.target, l.name)
			}
		} else {
			err = u.root.Symlink(filepath.FromSlash(l.target), rel)
			if err != nil {
				err = u.copyFile(path.Join(path.Dir(l.name), strings.ReplaceAll(l.target, `\`, "/")), l.name)
			}
		}
		if err != nil {
			return fmt.Errorf("couldn't make %s from the download: %w", l.name, err)
		}
	}
	return nil
}

// copyFile copies the file at from to to (both archive names), keeping
// its bits, where a link can't be made.
func (u *unpacker) copyFile(from, to string) error {
	in, err := u.root.Open(filepath.FromSlash(from))
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a file", from)
	}
	return u.file(to, in, fi.Mode()&0o111 != 0)
}

// Unzip unpacks zipPath into dir; exec says which files must be runnable.
//
// exec gets each file's name as it is in the zip, slash-separated and
// cleaned ("1461/Mac/Terraria Server.app/Contents/MacOS/TerrariaServer.bin.osx");
// it may be nil. Files the zip itself marks runnable stay runnable.
func Unzip(zipPath, dir string, exec func(name string) bool) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("the download %s isn't a zip file the app can open (%v)", filepath.Base(zipPath), err)
	}
	defer zr.Close()
	u, err := newUnpacker(dir)
	if err != nil {
		return err
	}
	defer u.close()
	for _, f := range zr.File {
		name, err := entryName(f.Name)
		if err != nil {
			return err
		}
		if name == "" || name == "__MACOSX" || strings.HasPrefix(name, "__MACOSX/") {
			continue // the top folder; a Mac's hidden resource files
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = u.dir(name)
		case mode&fs.ModeSymlink != 0:
			var target string
			target, err = readLinkEntry(f)
			if err == nil {
				err = linkProblem(name, target)
			}
			if err == nil {
				u.links = append(u.links, link{name: name, target: target})
			}
		case mode.IsRegular():
			var rc io.ReadCloser
			rc, err = f.Open()
			if err == nil {
				runnable := mode&0o111 != 0 || (exec != nil && exec(name))
				err = u.file(name, rc, runnable)
				rc.Close()
			}
		}
		if err != nil {
			return unpackError(zipPath, err)
		}
	}
	return unpackError(zipPath, u.makeLinks())
}

// readLinkEntry reads a symbolic link's target from a zip, where it is
// stored as the entry's contents.
func readLinkEntry(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 4096))
	return string(b), err
}

// Untar unpacks a .tar.gz into dir, keeping exec bits.
func Untar(tgzPath, dir string) error {
	f, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("the download %s isn't a .tar.gz file the app can open (%v)", filepath.Base(tgzPath), err)
	}
	defer gz.Close()
	u, err := newUnpacker(dir)
	if err != nil {
		return err
	}
	defer u.close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("the download %s is damaged (%v)", filepath.Base(tgzPath), err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := entryName(h.Name)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = u.dir(name)
		case tar.TypeReg:
			err = u.file(name, tr, h.Mode&0o111 != 0)
		case tar.TypeSymlink:
			err = linkProblem(name, h.Linkname)
			if err == nil {
				u.links = append(u.links, link{name: name, target: h.Linkname})
			}
		case tar.TypeLink:
			var target string
			target, err = entryName(h.Linkname)
			if err == nil && target == "" {
				err = fmt.Errorf("the download has a link to its own folder (%q): %w", h.Name, errUnsafe)
			}
			if err == nil {
				u.links = append(u.links, link{name: name, target: target, hard: true})
			}
		}
		if err != nil {
			return unpackError(tgzPath, err)
		}
	}
	return unpackError(tgzPath, u.makeLinks())
}

// unpackError adds which download couldn't be unpacked, unless the error
// already says what is wrong with it.
func unpackError(archive string, err error) error {
	if err == nil || errors.Is(err, errUnsafe) {
		return err
	}
	return fmt.Errorf("couldn't unpack %s: %w", filepath.Base(archive), err)
}
