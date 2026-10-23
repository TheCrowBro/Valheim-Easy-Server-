// Command fakevalheim imitates the Valheim dedicated server for testing and
// for trying the app without downloading the real server. It accepts the
// same arguments, writes a log in Valheim's format, keeps worlds in the
// Valheim 1.0 folder format (converting old .fwl/.db worlds like the real
// server), and saves the world when it gets Ctrl+C.
//
// Environment variables:
//
//	FAKE_VALHEIM_SIM=1        random players join, leave and die
//	FAKE_VALHEIM_SCRIPT=...   scripted events, e.g. "join:Astrid@1,leave:Astrid@5"
//	                          (seconds after the server is ready)
//	FAKE_VALHEIM_SPEED=60     run save and backup timers this many times faster
//	FAKE_VALHEIM_CRASH=10     exit with an error after this many seconds
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type server struct {
	name, world, password, saveDir string
	port                           int
	crossplay                      bool
	saveInterval                   time.Duration
	backups                        int
	backupShort                    time.Duration
	speed                          float64

	mu      sync.Mutex
	saveMu  sync.Mutex
	logf    *os.File
	players map[string]string // name -> steam id
	gen     int               // _main.<gen> files
	ballast [][]byte          // memory held per player
	stop    chan struct{}
}

func main() {
	s := &server{port: 2456, saveInterval: 1800 * time.Second, backups: 4, backupShort: 7200 * time.Second,
		players: map[string]string{}, speed: 1, stop: make(chan struct{})}
	logFile := ""
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch strings.ToLower(args[i]) {
		case "-name":
			s.name = next()
		case "-world":
			s.world = next()
		case "-password":
			s.password = next()
		case "-port":
			s.port, _ = strconv.Atoi(next())
		case "-savedir":
			s.saveDir = next()
		case "-logfile":
			logFile = next()
		case "-crossplay":
			s.crossplay = true
		case "-saveinterval":
			n, _ := strconv.Atoi(next())
			s.saveInterval = time.Duration(n) * time.Second
		case "-backups":
			s.backups, _ = strconv.Atoi(next())
		case "-backupshort":
			n, _ := strconv.Atoi(next())
			s.backupShort = time.Duration(n) * time.Second
		case "-modifier":
			next()
			next()
		case "-public", "-backuplong", "-preset", "-setkey", "-removekey", "-instanceid":
			next()
		}
	}
	if v, err := strconv.ParseFloat(os.Getenv("FAKE_VALHEIM_SPEED"), 64); err == nil && v > 0 {
		s.speed = v
	}
	if s.world == "" {
		s.world = "Dedicated"
	}
	if s.saveDir == "" {
		s.saveDir = "."
	}
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		s.logf = f
	}

	// Ctrl+C (and Ctrl+Break / close on Windows) saves and quits.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		s.shutdown()
	}()

	s.log("Valheim version: l-1.0.20 (network version 41)")
	s.log("Using savedir: " + s.saveDir)
	s.log(fmt.Sprintf("Fake server %q starting on port %d", s.name, s.port))
	s.ballast = append(s.ballast, touch(150<<20)) // a server uses memory even when empty
	seed := s.loadWorld()
	if s.password != "" && (strings.Contains(strings.ToLower(s.world), strings.ToLower(s.password)) ||
		strings.Contains(strings.ToLower(seed), strings.ToLower(s.password))) {
		s.log("Error bad password: the password can't be part of the world name or seed")
		os.Exit(1)
	}
	s.sleep(2 * time.Second)
	s.log("ZNet.LoadWorld: " + s.world)
	s.log("DungeonDB Start")
	s.sleep(1 * time.Second)
	s.log("Game server connected")
	code := ""
	if s.crossplay {
		s.log(fmt.Sprintf(`New session server "%s" that has join code , now 0 player(s)`, s.name))
		code = fmt.Sprintf("%06d", 100000+rand.Intn(899999))
		s.log(fmt.Sprintf(`Session "%s" registered with join code %s`, s.name, code))
	}

	go s.loop(code)
	if os.Getenv("FAKE_VALHEIM_SIM") == "1" {
		go s.simulate()
	}
	if script := os.Getenv("FAKE_VALHEIM_SCRIPT"); script != "" {
		go s.runScript(script)
	}
	if v, err := strconv.Atoi(os.Getenv("FAKE_VALHEIM_CRASH")); err == nil && v > 0 {
		go func() {
			time.Sleep(time.Duration(v) * time.Second)
			s.log("Crash!!! NullReferenceException: Object reference not set to an instance of an object")
			os.Exit(3)
		}()
	}
	go s.burnCPU()
	<-s.stop
	select {} // shutdown exits the process
}

func (s *server) log(msg string) {
	line := time.Now().Format("01/02/2006 15:04:05") + ": " + msg + "\n"
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logf != nil {
		s.logf.WriteString(line)
		// Unity also writes this noise after many lines.
		if rand.Intn(4) == 0 {
			s.logf.WriteString("(Filename: ./Runtime/Export/Debug/Debug.bindings.h Line: 35)\n\n")
		}
	} else {
		os.Stdout.WriteString(line)
	}
}

func (s *server) sleep(d time.Duration) {
	time.Sleep(time.Duration(float64(d) / s.speed))
}

func (s *server) worldsDir() string { return filepath.Join(s.saveDir, "worlds_local") }

// loadWorld finds, converts or creates the world and returns its seed.
func (s *server) loadWorld() string {
	dir := filepath.Join(s.worldsDir(), s.world)
	os.MkdirAll(s.worldsDir(), 0o755)
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		s.gen, _ = latestGen(dir)
		s.log("Loading world " + s.world)
		return seedOf(filepath.Join(dir, fmt.Sprintf("_main.%d.fwl2", s.gen)))
	}
	old := filepath.Join(s.worldsDir(), s.world)
	if data, err := os.ReadFile(old + ".fwl"); err == nil {
		s.log("World " + s.world + " is in the old format, converting it")
		stamp := time.Now().Format("20060102-150405")
		db, _ := os.ReadFile(old + ".db")
		os.WriteFile(filepath.Join(s.worldsDir(), s.world+"_backup_"+stamp+".fwl"), data, 0o644)
		os.WriteFile(filepath.Join(s.worldsDir(), s.world+"_backup_"+stamp+".db"), db, 0o644)
		os.Rename(old+".fwl", old+".fwl.old")
		os.Rename(old+".db", old+".db.old")
		s.gen = 1
		s.writeWorld(dir, data, len(db)+1000)
		return seedOf(old + ".fwl.old")
	}
	s.log("Creating new world " + s.world)
	seed := randomSeed()
	s.gen = 0
	s.writeWorld(dir, buildMeta(s.world, seed), 2000)
	return seed
}

var reGen = regexp.MustCompile(`^_main\.(\d+)\.fwl2$`)

func latestGen(dir string) (int, bool) {
	entries, _ := os.ReadDir(dir)
	best, ok := 0, false
	for _, e := range entries {
		if m := reGen.FindStringSubmatch(e.Name()); m != nil {
			if n, _ := strconv.Atoi(m[1]); n >= best {
				best, ok = n, true
			}
		}
	}
	return best, ok
}

// writeWorld writes the _main.<gen> files and some chunks.
func (s *server) writeWorld(dir string, meta []byte, dbSize int) {
	os.MkdirAll(dir, 0o755)
	g := s.gen
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("_main.%d.fwl2", g)), meta, 0o644)
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("_main.%d.db2", g)), bytes.Repeat([]byte{7}, dbSize), 0o644)
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("_main.%d.chunks", g)), []byte("chunk index"), 0o644)
	for i := 0; i < 6; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d_%02d__0_%d.chunk", i, i%3, g)), bytes.Repeat([]byte{byte(i)}, 4000+rand.Intn(4000)), 0o644)
	}
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("_main.%d.ok", g)), []byte("ok\n"), 0o644)
}

// save writes the next generation of the world files, like Valheim 1.0.
func (s *server) save() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	start := time.Now()
	dir := filepath.Join(s.worldsDir(), s.world)
	meta, _ := os.ReadFile(filepath.Join(dir, fmt.Sprintf("_main.%d.fwl2", s.gen)))
	if len(meta) == 0 {
		meta = buildMeta(s.world, randomSeed())
	}
	old := s.gen
	s.gen++
	for step := 1; step <= 5; step++ {
		s.log(fmt.Sprintf("World save (%d/%d) done", step, 5))
	}
	s.writeWorld(dir, meta, 3000+1000*s.gen)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), fmt.Sprintf(".%d.", old)) || strings.HasSuffix(e.Name(), fmt.Sprintf("_%d.chunk", old)) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	s.log(fmt.Sprintf("World saved ( %.3fms )", float64(time.Since(start).Microseconds())/1000))
}

// backup copies the world into a Valheim 1.0 style auto-backup folder.
func (s *server) backup() {
	src := filepath.Join(s.worldsDir(), s.world)
	dst := filepath.Join(s.worldsDir(), s.world+"_backup_auto-"+time.Now().Format("20060102-150405"))
	os.MkdirAll(dst, 0o755)
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(src, e.Name()))
		os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644)
	}
	// Keep only the newest backups.
	matches, _ := filepath.Glob(filepath.Join(s.worldsDir(), s.world+"_backup_auto-*"))
	sort.Strings(matches)
	for i := 0; i < len(matches)-s.backups; i++ {
		os.RemoveAll(matches[i])
	}
	s.log("Backed up world to " + filepath.Base(dst))
}

func (s *server) loop(code string) {
	saveTick := time.NewTicker(time.Duration(float64(s.saveInterval) / s.speed))
	backupTick := time.NewTicker(time.Duration(float64(s.backupShort) / s.speed))
	statusTick := time.NewTicker(time.Duration(float64(10*time.Minute) / s.speed))
	for {
		select {
		case <-saveTick.C:
			s.save()
		case <-backupTick.C:
			s.backup()
		case <-statusTick.C:
			s.mu.Lock()
			n := len(s.players)
			s.mu.Unlock()
			s.log(fmt.Sprintf("Connections %d ZDOS:%d  sent:0 recv:0", n, 100000+rand.Intn(50000)))
			if code != "" {
				s.log(fmt.Sprintf(`Session "%s" with join code %s and IP 203.0.113.7:%d is active with %d player(s)`, s.name, code, s.port, n))
			}
		}
	}
}

var names = []string{"Astrid", "Bjorn", "Freydis", "Ragnar", "Sigrid", "Ulf", "Ingrid", "Leif", "Solveig", "Torvald"}

func (s *server) join(name string) {
	s.mu.Lock()
	if _, ok := s.players[name]; ok || len(s.players) >= 10 {
		s.mu.Unlock()
		return
	}
	id := fmt.Sprintf("765611980000%05d", rand.Intn(99999))
	s.players[name] = id
	s.ballast = append(s.ballast, touch(60<<20))
	s.mu.Unlock()
	s.log("Got connection SteamID " + id)
	s.log("Got handshake from client " + id)
	s.log("Server: New peer connected,sending global keys")
	s.log(fmt.Sprintf("Got character ZDOID from %s : %d:1", name, -rand.Intn(99999999)))
}

func (s *server) leave(name string) {
	s.mu.Lock()
	id, ok := s.players[name]
	delete(s.players, name)
	if ok && len(s.ballast) > 1 {
		s.ballast = s.ballast[:len(s.ballast)-1]
	}
	s.mu.Unlock()
	if ok {
		s.log("Closing socket " + id)
		s.log("Destroying abandoned non persistent zdo 123:45 owner 6789")
	}
}

func (s *server) die(name string) {
	s.log(fmt.Sprintf("Got character ZDOID from %s : 0:0", name))
	time.Sleep(500 * time.Millisecond)
	s.log(fmt.Sprintf("Got character ZDOID from %s : %d:9", name, -rand.Intn(99999999)))
}

func (s *server) simulate() {
	time.Sleep(3 * time.Second)
	for {
		time.Sleep(time.Duration(4+rand.Intn(8)) * time.Second)
		s.mu.Lock()
		online := make([]string, 0, len(s.players))
		for n := range s.players {
			online = append(online, n)
		}
		s.mu.Unlock()
		switch r := rand.Intn(10); {
		case r < 5 && len(online) < 6:
			s.join(names[rand.Intn(len(names))])
		case r < 7 && len(online) > 1:
			s.leave(online[rand.Intn(len(online))])
		case r < 8 && len(online) > 0:
			s.die(online[rand.Intn(len(online))])
		case r < 9:
			id := fmt.Sprintf("765611980000%05d", rand.Intn(99999))
			s.log("Got connection SteamID " + id)
			s.log("Peer " + id + " has wrong password")
			s.log("Closing socket " + id)
		}
	}
}

func (s *server) runScript(script string) {
	type ev struct {
		at         float64
		verb, name string
	}
	var evs []ev
	for _, part := range strings.Split(script, ",") {
		verbName, at, _ := strings.Cut(strings.TrimSpace(part), "@")
		verb, name, _ := strings.Cut(verbName, ":")
		sec, _ := strconv.ParseFloat(at, 64)
		evs = append(evs, ev{sec, verb, name})
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].at < evs[j].at })
	start := time.Now()
	for _, e := range evs {
		time.Sleep(time.Until(start.Add(time.Duration(e.at * float64(time.Second)))))
		switch e.verb {
		case "join":
			s.join(e.name)
		case "leave":
			s.leave(e.name)
		case "die":
			s.die(e.name)
		case "save":
			s.save()
		case "count":
			s.mu.Lock()
			n := len(s.players)
			s.mu.Unlock()
			s.log(fmt.Sprintf("Connections %d ZDOS:1000  sent:0 recv:0", n))
		}
	}
}

// burnCPU uses some CPU per player so the stats page has something to show.
func (s *server) burnCPU() {
	for {
		s.mu.Lock()
		n := len(s.players)
		s.mu.Unlock()
		busy := time.Duration(3+6*n) * time.Millisecond
		end := time.Now().Add(busy)
		x := 0
		for time.Now().Before(end) {
			x++
		}
		time.Sleep(100*time.Millisecond - busy/2)
	}
}

// shutdown saves the world and exits, like Valheim on Ctrl+C.
func (s *server) shutdown() {
	s.log("OnApplicationQuit")
	s.log("Game - OnApplicationQuit")
	time.Sleep(800 * time.Millisecond)
	s.save()
	s.log("Net scene destroyed")
	if s.logf != nil {
		s.logf.Sync()
	}
	os.Exit(0)
}

func touch(n int) []byte {
	b := make([]byte, n)
	for i := 0; i < len(b); i += 4096 {
		b[i] = 1
	}
	return b
}

func randomSeed() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func writeStr(w *bytes.Buffer, s string) {
	n := len(s)
	for n >= 0x80 {
		w.WriteByte(byte(n) | 0x80)
		n >>= 7
	}
	w.WriteByte(byte(n))
	w.WriteString(s)
}

func buildMeta(name, seed string) []byte {
	var body bytes.Buffer
	binary.Write(&body, binary.LittleEndian, int32(37))
	writeStr(&body, name)
	writeStr(&body, seed)
	binary.Write(&body, binary.LittleEndian, int32(rand.Int31()))
	binary.Write(&body, binary.LittleEndian, rand.Int63())
	binary.Write(&body, binary.LittleEndian, int32(2))
	body.WriteByte(1)
	binary.Write(&body, binary.LittleEndian, int32(0))
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, int32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// seedOf reads the seed name from a metadata file (best effort).
func seedOf(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 10 {
		return ""
	}
	r := bytes.NewReader(b[8:])
	readStr := func() string {
		n, err := r.ReadByte()
		if err != nil || int(n) > r.Len() {
			return ""
		}
		buf := make([]byte, n)
		r.Read(buf)
		return string(buf)
	}
	readStr() // name
	return readStr()
}
