package vintagestory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"valheim-easy-server/internal/platform"
)

// The server reads its settings from serverconfig.json in its data folder
// (--dataPath, the instance's saves folder). The app writes the keys it
// decides just before each start, and only then: the server writes the
// whole file back once it has started (and when /serverconfig changes
// something), so it is never edited while the server runs (FACTS.md).
//
// The app never writes the file from scratch. A file with only the app's
// keys stops the server at once ("You have configured a default group code
// suplayer but no such group exists! Killing server", FACTS.md): the
// player roles and everything else must be there. So when there is no file
// the app has the installed server write its own defaults first
// (--genconfig), and from then on it changes only its keys and keeps every
// other key, in its place, as the server or the owner left it.

const configName = "serverconfig.json"

// jsonObject is a JSON object that keeps its keys in order and every value
// exactly as written, so changing a few keys leaves the rest of the file as
// it was.
type jsonObject struct {
	keys []string
	vals map[string]json.RawMessage
}

var bom = []byte("\ufeff")

func parseObject(b []byte) (*jsonObject, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(b, bom)))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		if err == nil {
			err = errors.New("it isn't a JSON object")
		}
		return nil, err
	}
	o := &jsonObject{vals: map[string]json.RawMessage{}}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, errors.New("it ends too soon")
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("there is more after its end")
	}
	return o, nil
}

// set gives key a value, adding the key at the end if it's new.
func (o *jsonObject) set(key string, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = bytes.TrimSpace(buf.Bytes())
}

func (o *jsonObject) has(key string) bool {
	_, ok := o.vals[key]
	return ok
}

// object is the object under key; a missing or null one starts empty.
func (o *jsonObject) object(key string) (*jsonObject, error) {
	v, ok := o.vals[key]
	if !ok || string(bytes.TrimSpace(v)) == "null" {
		return &jsonObject{vals: map[string]json.RawMessage{}}, nil
	}
	return parseObject(v)
}

func (o *jsonObject) setObject(key string, c *jsonObject) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = c.marshal(false)
}

// marshal writes the object back, indented with two spaces as the server
// writes it when indent is true.
func (o *jsonObject) marshal(indent bool) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	if !indent {
		var c bytes.Buffer
		if json.Compact(&c, b.Bytes()) == nil {
			return c.Bytes()
		}
		return b.Bytes()
	}
	var out bytes.Buffer
	if json.Indent(&out, b.Bytes(), "", "  ") != nil {
		return b.Bytes()
	}
	return out.Bytes()
}

// configValues are the keys the app decides.
type configValues struct {
	ServerName string
	Port       int
	MaxClients int
	Password   string
	World      string
	SaveFile   string // the world's .vcdbs, absolute
	New        bool   // the world is made at this start
	Seed       string // new world only; "" lets the server pick
	Style      playStyle
}

// applyConfig sets the app's keys in a serverconfig.json the server wrote.
//
//   - WhitelistMode 1 is "Off" (0 is "Default", which on a dedicated server
//     since 1.20 means on, and 2 is "On"; FACTS.md). This round has no
//     screen for the whitelist, so friends are let in by the password.
//   - AdvertiseServer false keeps the server off the public server list,
//     and Upnp false leaves the router to the app (spec 6.5).
//   - The world: the server opens SaveFileLocation, or makes it there from
//     WorldName, Seed and the play style when it doesn't exist. A world it
//     opens keeps the style and seed it was made with (FACTS.md), so those
//     are written only for a new world.
func applyConfig(b []byte, v configValues) ([]byte, error) {
	o, err := parseObject(b)
	if err != nil {
		return nil, err
	}
	o.set("ServerName", v.ServerName)
	o.set("Port", v.Port)
	o.set("MaxClients", v.MaxClients)
	o.set("Password", v.Password)
	o.set("WhitelistMode", 1)
	if o.has("OnlyWhitelisted") {
		// The setting older versions had before WhitelistMode.
		o.set("OnlyWhitelisted", false)
	}
	o.set("AdvertiseServer", false)
	o.set("Upnp", false)
	wc, err := o.object("WorldConfig")
	if err != nil {
		return nil, fmt.Errorf("its WorldConfig %v", err)
	}
	wc.set("WorldName", v.World)
	wc.set("SaveFileLocation", v.SaveFile)
	if v.New {
		if v.Seed == "" {
			wc.set("Seed", nil)
		} else {
			wc.set("Seed", v.Seed)
		}
		wc.set("PlayStyle", v.Style.code)
		wc.set("PlayStyleLangCode", v.Style.langCode)
		wc.set("WorldType", v.Style.worldType)
	}
	o.setObject("WorldConfig", wc)
	return o.marshal(true), nil
}

// writeConfig writes the settings for one start: the server's own file
// with the app's keys set. A file the server would stop on every time (the
// owner edited it by hand and broke it) is set aside, never thrown away,
// and the server writes a new one, so the next start works.
func (s *server) writeConfig(ctx context.Context, dll string, v configValues, step func(string, float64)) error {
	path := filepath.Join(s.env.Dirs.Saves, configName)
	b, err := os.ReadFile(path)
	if err == nil {
		out, perr := configFor(b, v)
		if perr == nil {
			return writePrivate(path, out)
		}
		aside := path + ".damaged-" + time.Now().Format("2006-01-02_15-04-05")
		if rerr := os.Rename(path, aside); rerr != nil {
			return fmt.Errorf("The server's settings file %s can't be used (%v), and it couldn't be set aside: %v", path, perr, rerr)
		}
		step("The server's settings file couldn't be used ("+perr.Error()+"), so it was set aside as "+filepath.Base(aside)+" and a new one made.", -1)
		s.log.Printf("serverconfig.json can't be used (%v); set aside as %s", perr, aside)
		err = fs.ErrNotExist
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Couldn't read the server's settings file: %v", err)
	}
	if err := s.genConfig(ctx, dll); err != nil {
		return err
	}
	if b, err = os.ReadFile(path); err != nil {
		return fmt.Errorf("Couldn't read the server's settings file: %v", err)
	}
	out, err := configFor(b, v)
	if err != nil {
		// The server has only just written this file itself, so setting
		// it aside again would go round in circles.
		return fmt.Errorf("The settings file the Vintage Story server made, %s, can't be used: %v", path, err)
	}
	return writePrivate(path, out)
}

// configFor is the file for one start, or why the file b can't be used.
func configFor(b []byte, v configValues) ([]byte, error) {
	if err := usable(b); err != nil {
		return nil, err
	}
	return applyConfig(b, v)
}

// usable says why a serverconfig.json can't be used, or nil. Besides being
// JSON it must have the player roles, and its DefaultRoleCode must be one
// of them: otherwise the server stops at once ("You have configured a
// default group code … but no such group exists! Killing server",
// FACTS.md), on every start, because nothing else changes the file. The
// app must also be able to set the world in its WorldConfig.
//
// A value Go reads but the server can't (a word where it wants a number)
// isn't caught here: that would take the server's whole format. The exit
// hint for "Failed to read serverconfig.json" tells the owner what to do.
func usable(b []byte) error {
	o, err := parseObject(b)
	if err != nil {
		return err
	}
	var roles []struct{ Code *string }
	if err := json.Unmarshal(o.vals["Roles"], &roles); o.has("Roles") && err != nil {
		return errors.New("its player roles can't be read")
	}
	if len(roles) == 0 {
		return errors.New("it has no player roles")
	}
	// Without the key the server uses its own default, suplayer
	// (minimal-config.log). A null would leave it no role at all.
	def := "suplayer"
	if raw, ok := o.vals["DefaultRoleCode"]; ok {
		var code *string
		if json.Unmarshal(raw, &code) != nil || code == nil {
			return errors.New("its DefaultRoleCode isn't a role's code")
		}
		def = *code
	}
	if !slices.ContainsFunc(roles, func(r struct{ Code *string }) bool { return r.Code != nil && *r.Code == def }) {
		return fmt.Errorf("its DefaultRoleCode %q isn't one of its player roles", def)
	}
	if _, err := o.object("WorldConfig"); err != nil {
		return errors.New("its WorldConfig isn't a JSON object")
	}
	return nil
}

// genConfig has the installed server write its default serverconfig.json
// and exit (--genconfig, which takes well under a second; FACTS.md).
func (s *server) genConfig(ctx context.Context, dll string) error {
	if err := os.MkdirAll(s.env.Dirs.Saves, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	var err error
	for try := 0; try < 5; try++ {
		cmd := exec.CommandContext(ctx, s.Program(), dll, "--dataPath", s.env.Dirs.Saves, "--genconfig")
		cmd.Dir = filepath.Dir(dll)
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.SysProcAttr = platform.HiddenProcAttr()
		out.Reset()
		if err = platform.StartCmd(cmd); err == nil {
			err = cmd.Wait()
			break
		}
		// Right after an install the program file can still be open for
		// writing in another process that the app was starting at that
		// moment (Linux's "text file busy"): try again shortly.
		if !errors.Is(err, syscall.ETXTBSY) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, serr := os.Stat(filepath.Join(s.env.Dirs.Saves, configName)); err != nil || serr != nil {
		last := strings.TrimSpace(out.String())
		if i := strings.LastIndexByte(last, '\n'); i >= 0 {
			last = last[i+1:]
		}
		if err == nil {
			err = serr
		}
		return fmt.Errorf("The Vintage Story server couldn't write its default settings (%v). %s", err, last)
	}
	return nil
}

// writePrivate writes a file in one piece (a temp file, then a rename),
// readable only by this user, as os.CreateTemp makes it: it holds the
// server's password (spec I9). The server writes the file again once it
// has started, with its own permissions (FACTS.md).
func writePrivate(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
