package zomboid

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"valheim-easy-server/internal/game"
)

// How the server is started, read from the files Steam installs with it.
//
// On Windows Steam runs StartServer64.bat (Steam's launch settings for the
// app, FACTS.md). It sets PZ_CLASSPATH and runs the bundled Java with
// options, the main class zombie.network.GameServer and "-statistic 0".
// The app starts that Java itself, so the one process it watches is the
// server, and so it needs those options: it reads them from the .bat at
// every start, so whatever an update changes there is picked up. It leaves
// out the .bat's -Xms/-Xmx (16 GB, more than many PCs have) and gives its
// own -Xmx from the "Java memory" setting instead.
//
// ProjectZomboid64.json says the same for the native launcher (the one the
// Linux script runs); it is the fallback, and the first choice on Linux,
// where there is no .bat.

const (
	batName  = "StartServer64.bat"
	jsonName = "ProjectZomboid64.json"
)

// errNoLaunch is what an install or a start says when neither file can be
// read: the download is broken or the server's layout has changed.
var errNoLaunch = errors.New("Couldn't read how to start the Project Zomboid server. Try Repair.")

// launch is how to start the server's Java, without the app's own
// additions.
type launch struct {
	Source    string   // the file it was read from, for the log
	JVM       []string // Java's options, without -Xms/-Xmx and the class path
	ClassPath string   // as the launcher gives it (relative to the server folder)
	Main      string   // zombie.network.GameServer
	Game      []string // the launcher's own arguments for the server ("-statistic", "0")
	// Env is what the launcher sets in the environment of the Java it
	// starts, as NAME=value: the .bat's SET lines (PZ_CLASSPATH). Spec 2.4:
	// whatever a skipped script sets goes into the plan's environment.
	Env []string
}

// readLaunch reads how to start the server from its folder, trying the
// file goos's launcher uses first. Either file that parses will do.
func readLaunch(serverDir, goos string) (launch, error) {
	order := []string{batName, jsonName}
	if goos != "windows" {
		order = []string{jsonName, batName}
	}
	var problems []string
	for _, name := range order {
		b, err := os.ReadFile(filepath.Join(serverDir, name))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		var l launch
		if name == batName {
			l, err = parseBat(string(b))
		} else {
			l, err = parseLaunchJSON(b, goos)
		}
		if err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		l.Source = name
		return l, nil
	}
	return launch{}, fmt.Errorf("%w (%s)", errNoLaunch, strings.Join(problems, "; "))
}

// dropJVM says whether a Java option is one the app sets itself: the heap
// sizes, which the "Java memory" setting replaces.
func dropJVM(opt string) bool {
	lower := strings.ToLower(opt)
	return strings.HasPrefix(lower, "-xms") || strings.HasPrefix(lower, "-xmx")
}

var (
	reSet    = regexp.MustCompile(`(?i)^set\s+(?:"([^"=]+)=([^"]*)"|([^=\s]+)=(.*))$`)
	reVar    = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)
	reBatArg = regexp.MustCompile(`^%(?:[0-9]|\*)$`)
	// reBatSelf is the .bat's own path with modifiers: %~dp0 is the folder
	// it is in, with a backslash at the end.
	reBatSelf = regexp.MustCompile(`(?i)%~([a-z]*)0`)
)

// parseBat reads the Java command from a Windows .bat as cmd.exe would
// run it: SET lines give variables, which %NAME% then stands for, and the
// line that names the main class is the command. Its first word (the
// Java program) is left out: the app runs its own Program(). "%1 %2", the
// .bat's own arguments, are left out too. %~dp0, the .bat's own folder,
// becomes ".\": the plan runs the server in that folder, so the paths mean
// the same, and the app never hands Java a "%~dp0" it would take as part
// of a folder's name. Every SET is also kept for the plan's environment.
func parseBat(text string) (launch, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	vars := map[string]string{}
	var setOrder []string // the names SET, as first written
	expand := func(s string) (string, bool) {
		ok := true
		s = reBatSelf.ReplaceAllStringFunc(s, func(m string) string {
			if strings.EqualFold(m, "%~dp0") {
				return `.\`
			}
			ok = false // another part of the .bat's own path: not worked out
			return m
		})
		out := reVar.ReplaceAllStringFunc(s, func(m string) string {
			v, found := vars[strings.ToUpper(m[1:len(m)-1])]
			if !found {
				ok = false
				return m
			}
			return v
		})
		return out, ok
	}
	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		line = strings.TrimSpace(strings.TrimPrefix(line, "@"))
		if line == "" || strings.HasPrefix(line, "::") || strings.EqualFold(firstWord(line), "rem") {
			continue
		}
		if m := reSet.FindStringSubmatch(line); m != nil {
			name, value := m[1], m[2]
			if name == "" {
				name, value = m[3], m[4]
			}
			value, _ = expand(strings.TrimSpace(value))
			if _, seen := vars[strings.ToUpper(name)]; !seen {
				setOrder = append(setOrder, name)
			}
			vars[strings.ToUpper(name)] = value
			continue
		}
		words := splitCmd(line)
		at := -1
		for i, w := range words {
			if w == mainClass {
				at = i
				break
			}
		}
		if at < 1 {
			continue
		}
		if prog := strings.ToLower(filepath.Base(strings.ReplaceAll(words[0], `\`, "/"))); prog != "java.exe" && prog != "java" {
			return launch{}, fmt.Errorf("the line naming %s runs %q, not Java", mainClass, words[0])
		}
		l := launch{Main: mainClass}
		jvm := words[1:at]
		for i := 0; i < len(jvm); i++ {
			w, ok := expand(jvm[i])
			if !ok {
				return launch{}, fmt.Errorf("%q names a variable the .bat doesn't set", jvm[i])
			}
			switch lw := strings.ToLower(w); {
			case lw == "-cp" || lw == "-classpath" || lw == "--class-path":
				if i+1 >= len(jvm) {
					return launch{}, errors.New("the class path is missing after " + w)
				}
				i++
				cp, ok := expand(jvm[i])
				if !ok {
					return launch{}, fmt.Errorf("the class path %q names a variable the .bat doesn't set", jvm[i])
				}
				l.ClassPath = cp
			case lw == "-jar":
				return launch{}, errors.New("it runs a jar, not the server's main class")
			case dropJVM(w):
			default:
				l.JVM = append(l.JVM, w)
			}
		}
		for _, w := range words[at+1:] {
			if reBatArg.MatchString(w) {
				continue
			}
			w, ok := expand(w)
			if !ok {
				return launch{}, fmt.Errorf("%q names a variable the .bat doesn't set", w)
			}
			l.Game = append(l.Game, w)
		}
		if l.ClassPath == "" {
			return launch{}, errors.New("its Java command has no class path")
		}
		for _, name := range setOrder {
			// "SET NAME=" with nothing after it removes the variable,
			// so it adds nothing.
			if v := vars[strings.ToUpper(name)]; v != "" {
				l.Env = append(l.Env, name+"="+v)
			}
		}
		return l, nil
	}
	return launch{}, fmt.Errorf("no line runs %s", mainClass)
}

func firstWord(s string) string {
	w, _, _ := strings.Cut(s, " ")
	return w
}

// splitCmd splits a command line into words the way cmd.exe hands them to
// a program: spaces and tabs separate words, except inside double quotes,
// which are then removed.
func splitCmd(line string) []string {
	var words []string
	var cur strings.Builder
	in, have := false, false
	for _, r := range line {
		switch {
		case r == '"':
			in, have = !in, true
		case (r == ' ' || r == '\t') && !in:
			if have {
				words = append(words, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		words = append(words, cur.String())
	}
	return words
}

// launchJSON is ProjectZomboid64.json: the native launcher's settings.
type launchJSON struct {
	MainClass string   `json:"mainClass"`
	ClassPath []string `json:"classpath"`
	VMArgs    []string `json:"vmArgs"`
	// Windows adds options by Windows version: "7" and "10" (FACTS.md).
	Windows map[string]struct {
		VMArgs []string `json:"vmArgs"`
	} `json:"windows"`
}

// parseLaunchJSON reads ProjectZomboid64.json. Its class path is a list,
// joined with the system's separator; an entry "java/." means the folder
// java/. On Windows the options for Windows 10 are added (the app runs
// only on Windows 10 and 11), or else those for the newest version listed.
func parseLaunchJSON(b []byte, goos string) (launch, error) {
	var j launchJSON
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\ufeff")), &j); err != nil {
		return launch{}, err
	}
	main := strings.ReplaceAll(strings.TrimSpace(j.MainClass), "/", ".")
	if main != mainClass {
		return launch{}, fmt.Errorf("its main class is %q, not %s", j.MainClass, mainClass)
	}
	var cp []string
	for _, e := range j.ClassPath {
		if e = strings.TrimSpace(e); e != "" {
			if strings.HasSuffix(e, "/.") {
				e = strings.TrimSuffix(e, ".")
			}
			cp = append(cp, e)
		}
	}
	if len(cp) == 0 {
		return launch{}, errors.New("it has no class path")
	}
	sep := ":"
	if goos == "windows" {
		sep = ";"
	}
	l := launch{Main: mainClass, ClassPath: strings.Join(cp, sep)}
	opts := append([]string(nil), j.VMArgs...)
	if goos == "windows" && len(j.Windows) > 0 {
		key := "10"
		if _, ok := j.Windows[key]; !ok {
			keys := make([]string, 0, len(j.Windows))
			for k := range j.Windows {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(a, b int) bool { return versionLess(keys[a], keys[b]) })
			key = keys[len(keys)-1]
		}
		opts = append(opts, j.Windows[key].VMArgs...)
	}
	for _, o := range opts {
		if o = strings.TrimSpace(o); o != "" && !dropJVM(o) {
			l.JVM = append(l.JVM, o)
		}
	}
	return l, nil
}

// versionLess orders Windows version keys ("7" < "10") as numbers.
func versionLess(a, b string) bool {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	if ea == nil && eb == nil {
		return na < nb
	}
	return a < b
}

// checkedInstaller is the real install: SteamCMD, then a check that the
// app can read how to start what it installed, so a broken or changed
// download says so at once rather than at the first start.
type checkedInstaller struct {
	inner  game.Installer
	server string
	goos   string
	log    *log.Logger
}

// Install says only errNoLaunch when the launch files can't be read, the
// spec's exact sentence (5.6), as Plan does: what went wrong with each
// file is raw system text, so it goes to the game's log for whoever looks
// into it, not into the message the owner sees.
func (i *checkedInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if err := i.inner.Install(ctx, repair, progress); err != nil {
		return err
	}
	if _, err := readLaunch(i.server, i.goos); err != nil {
		i.log.Printf("after the install, reading the launch files: %v", err)
		return errNoLaunch
	}
	return nil
}

// The real Build 42 launch files, as Steam installs them on Windows (their
// bytes match the sizes and SHA-256 sums recorded on a real install,
// FACTS.md). The fake install puts them beside the stand-in, so in fake
// mode the plan reads the very files it reads on the owner's PC.
var (
	//go:embed testdata/launch/StartServer64.bat
	fakeBat []byte
	//go:embed testdata/launch/ProjectZomboid64.json
	fakeJSON []byte
)

// fakeInstaller is install.Fake plus the launch files.
type fakeInstaller struct {
	inner  game.Installer
	server string
}

func (i *fakeInstaller) Install(ctx context.Context, repair bool, progress func(game.Progress)) error {
	if err := i.inner.Install(ctx, repair, progress); err != nil {
		return err
	}
	for name, b := range map[string][]byte{batName: fakeBat, jsonName: fakeJSON} {
		if err := os.WriteFile(filepath.Join(i.server, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
