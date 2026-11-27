package main

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The windrose personality stands in for the Windrose dedicated server,
// R5/Binaries/Win64/WindroseServer-Win64-Shipping(.exe), as far as the app
// can see it (internal/games/windrose/FACTS.md has the real one's facts):
//
//	WindroseServer-Win64-Shipping -log
//
//   - It reads R5/ServerDescription.json beside its install, and makes it
//     on its first start (an identity, an invite code, a first world). It
//     writes it back with the world it loaded, as the real one does.
//   - It writes its log itself, in Unreal Engine's format, to
//     R5/Saved/Logs/R5.log (an older one is renamed R5-backup-<date>.log
//     first): the settings it read, its invite code and password, the
//     ready line, and Windrose's account lines for joins and leaves with
//     the lists of accounts below them.
//   - A world is RocksDB_v2/<version>/Worlds/<id>/ (WorldDescription.json
//     and database files) and RocksDB_v2_Backups/<version>/Worlds/<id>/
//     (zips, the newest ending in _Latest.zip) under
//     R5/Saved/SaveProfiles/Default. A WorldIslandId with no world makes a
//     new one with that id.
//   - It saves a backup every minute (on its own clock) when players have
//     changed something, and on Ctrl+C: "*** INTERRUPTED *** : CTRL-C TO
//     FORCE QUIT", the exit backup, the accounts, "Log file closed". A
//     second Ctrl+C quits at once without saving, as Unreal's does.
//   - It opens no ports: friends reach the real one through Windrose's
//     online service.
//
// fakeserver.json's options:
//
//	"ownWorldId": "true"  a missing world is made under a new id of the
//	                      server's own, written back to the file
//	"dumpsOnly": "true"   joins and leaves show only in the lists of
//	                      accounts, with none of the account lines

func init() {
	register(&Personality{
		ID:      "windrose",
		Version: "0.10.0.9.32",
		MemMB:   48,
		Match:   func(c Call) bool { return c.Program == "windroseserver-win64-shipping" },
		Run:     runWindrose,
	})
}

// wrSaveVersion is the game-version folder worlds are kept under.
const wrSaveVersion = "0.10.0"

type wrAccount struct {
	name, id, session, state string
	joined, ready, left      time.Time
	farewell                 string
}

type wrServer struct {
	s          *Server
	root       string // the install folder
	descPath   string
	saves      string // R5/Saved/SaveProfiles/Default
	desc       map[string]any
	pers       map[string]any
	island     string
	frame      int
	started    time.Time
	dirty      bool
	generation int
	stopping   bool

	connected    []*wrAccount
	disconnected []*wrAccount
}

func runWindrose(s *Server) {
	w := &wrServer{s: s, started: time.Now()}
	// The program is <install>/R5/Binaries/Win64/<exe>.
	w.root = filepath.Dir(filepath.Dir(filepath.Dir(s.call.Dir)))
	w.descPath = filepath.Join(w.root, "R5", "ServerDescription.json")
	w.saves = filepath.Join(w.root, "R5", "Saved", "SaveProfiles", "Default")

	logPath := filepath.Join(w.root, "R5", "Saved", "Logs", "R5.log")
	if st, err := os.Stat(logPath); err == nil {
		os.Rename(logPath, filepath.Join(filepath.Dir(logPath), "R5-backup-"+st.ModTime().UTC().Format("2006.01.02-15.04.05")+".log"))
	}
	s.LogTo(logPath)
	s.OnCrash(func() {
		w.line("LogWindows", "Error", "appError called: Fatal error: [File:D:\\Source\\Build\\work\\gameRepoCheckoutDir\\Source\\R5DataKeepers\\Private\\R5DataKeeperForServer.cpp] [Line: 146]")
		w.line("LogOutputDevice", "Error", "=== Crash Stack Trace: ===")
	})
	s.Log("\ufeffLog file open, " + time.Now().UTC().Format("01/02/06 15:04:05"))
	w.line("LogInit", "", "Build: ++R5+release-"+s.Version())
	w.line("LogInit", "", "Command Line: "+strings.Join(s.Args().List(), " "))

	w.loadDescription()
	s.Sleep(3 * time.Second) // the engine starting up
	w.loadWorld()
	w.saveDescription()
	w.r5("R5LogCoopProxy", "", "UR5CoopProxyServer::Init", "Server initialized. CurrentIslandId "+w.island, `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:101`)
	w.r5("R5LogBLDalAQ", "", "R5BLDalAsyncQueue::LoadDb", "[M:  -1:    1:  loadDB] Successfully loaded DB "+wrSaveVersion+"/Worlds/"+w.island+". IdentifierId", `Plugins\R5BusinessLogicCore\Source\R5BLCommon\Cpp\R5DataAbstractionLayer\R5BLDalAsyncQueue.cpp:107`)
	s.Sleep(2 * time.Second) // loading the world
	w.r5("R5LogCoopProxy", "", "UR5CoopProxyServer::OnServerRegistered", "Server registration finished successfully. BLSessionId "+randomHex(16), `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:420`)
	w.r5("R5LogCoopProxy", "", "...CoopProxyServer::PrintServerConnectionInfo", ">= Server Connection Info =================================================================================", `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:450`)
	w.r5("R5LogCoopProxy", "", "...CoopProxyServer::PrintServerConnectionInfo", "> InviteCode         : "+w.str("InviteCode"), `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:451`)
	w.r5("R5LogCoopProxy", "", "...CoopProxyServer::PrintServerConnectionInfo", "> Password           : "+w.str("Password"), `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:452`)
	w.r5("R5LogCoopProxy", "", "...CoopProxyServer::PrintServerConnectionInfo", ">==========================================================================================================", `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:453`)
	w.r5("R5LogDataKeeper", "", "IR5DataKeeperStateLogger::LogSetState", "Server. Change state LoadedIslandData => WaitingForFirstAccount in 359.000 ms", `Source\R5DataKeepers\Private\R5DataKeeperStateLogger.cpp:40`)

	s.OnStop(w.interrupted)
	s.OnStep(w.step)
	s.Every(time.Minute, func() {
		if w.dirty && !w.stopping {
			w.backup()
		}
	})
	s.Ready(w.r5text("R5LogCoopProxy", "", "...ProxyServer::SetIsReadyForHostOwnerConnect", "Host server is ready for owner to connect. Semaphore R5ServerSemaphore_"+randomHex(8), `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:170`))
}

// line writes one log line in Unreal's format: its time (UTC) and frame,
// then the category.
func (w *wrServer) line(category, verbosity, msg string) {
	w.s.Log(w.prefix() + category + ": " + verbosityPart(verbosity) + msg)
}

func (w *wrServer) prefix() string {
	w.frame++
	now := time.Now().UTC()
	return fmt.Sprintf("[%s:%03d][%3d]", now.Format("2006.01.02-15.04.05"), now.Nanosecond()/1e6, w.frame%1000)
}

func verbosityPart(v string) string {
	if v == "" {
		return ""
	}
	return v + ": "
}

// r5 writes one of Windrose's own lines: category and level padded, the
// frame, the function, the message and where in the source it is. A
// message of several lines goes on below, its source at the end of its
// last line.
func (w *wrServer) r5(category, verbosity, fn, msg, src string) {
	w.s.Log(w.r5text(category, verbosity, fn, msg, src))
}

func (w *wrServer) r5text(category, verbosity, fn, msg, src string) string {
	head := fmt.Sprintf("%-30s", category+": "+verbosityPart(verbosity))
	if len(fn) > 47 {
		fn = "..." + fn[len(fn)-44:]
	}
	source := `[D:\Source\Build\work\gameRepoCheckoutDir\` + src + `]`
	text := w.prefix() + head + fmt.Sprintf("[%06d] %-51s", w.frame, fn)
	if first, rest, multi := strings.Cut(msg, "\n"); multi {
		return text + first + "\n" + rest + " " + source
	}
	return text + fmt.Sprintf("%-100s", msg) + source
}

// loadDescription reads ServerDescription.json, or makes it as the real
// server does at its first start.
func (w *wrServer) loadDescription() {
	b, err := os.ReadFile(w.descPath)
	if err == nil {
		b = bytes.TrimPrefix(b, []byte("\ufeff"))
		if json.Unmarshal(b, &w.desc) == nil {
			if p, ok := w.desc["ServerDescription_Persistent"].(map[string]any); ok {
				w.pers = p
			}
		}
		if w.pers == nil {
			w.r5("R5LogCoopProxy", "Error", "...verDescriptionManager::LoadServerDescription", "Server description json is broken. Cannot find or parse 'ServerDescription_Persistent' field. Json:\n"+string(b), `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopServerDescriptionManager.cpp:50`)
		}
	}
	if w.pers == nil {
		w.r5("R5LogCoopProxy", "", "...verDescriptionManager::LoadServerDescription", "Create default server description at '"+filepath.ToSlash(w.descPath)+"'", `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopServerDescriptionManager.cpp:40`)
		w.pers = map[string]any{
			"PersistentServerId":               strings.ToUpper(randomHex(16)),
			"InviteCode":                       randomHex(4),
			"IsPasswordProtected":              false,
			"Password":                         "",
			"ServerName":                       "",
			"WorldIslandId":                    "",
			"MaxPlayerCount":                   4,
			"UserSelectedRegion":               "",
			"P2pProxyAddress":                  "127.0.0.1",
			"UseDirectConnection":              false,
			"DirectConnectionServerAddress":    "",
			"DirectConnectionServerPort":       7777,
			"DirectConnectionProxyAddress":     "0.0.0.0",
			"AutoLoadLatestBackupIfHasBroken":  true,
			"CanLaunchMultipleServerInstances": false,
		}
		w.desc = map[string]any{"Version": 1, "DeploymentId": w.s.Version() + "-00000000", "ServerDescription_Persistent": w.pers}
	}
	w.desc["DeploymentId"] = w.s.Version() + "-00000000"
	if (w.str("Password") != "") != w.boolean("IsPasswordProtected") {
		w.r5("R5LogCoopProxy", "Warning", "...verDescriptionManager::LoadServerDescription", "IsPasswordProtected doesn't match Password: the server may not behave as expected", `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopServerDescriptionManager.cpp:60`)
	}
}

func (w *wrServer) str(key string) string {
	v, _ := w.pers[key].(string)
	return v
}

func (w *wrServer) boolean(key string) bool {
	v, _ := w.pers[key].(bool)
	return v
}

// saveDescription writes the file back, tab-indented with Windows line
// endings as the real server does, and says so in the log with its text.
func (w *wrServer) saveDescription() {
	b, _ := json.MarshalIndent(w.desc, "", "\t")
	os.MkdirAll(filepath.Dir(w.descPath), 0o755)
	os.WriteFile(w.descPath, []byte(strings.ReplaceAll(string(b), "\n", "\r\n")+"\r\n"), 0o644)
	w.r5("R5LogCoopProxy", "Verbose", "...verDescriptionManager::SaveServerDescription",
		"Saved server description. FilePath '"+filepath.ToSlash(w.descPath)+"'. Description\n"+string(b),
		`Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopServerDescriptionManager.cpp:77`)
}

func (w *wrServer) worldDir() string {
	return filepath.Join(w.saves, "RocksDB_v2", wrSaveVersion, "Worlds", w.island)
}

func (w *wrServer) backupDir() string {
	return filepath.Join(w.saves, "RocksDB_v2_Backups", wrSaveVersion, "Worlds", w.island)
}

// loadWorld loads the world WorldIslandId names, or makes it.
func (w *wrServer) loadWorld() {
	w.island = w.str("WorldIslandId")
	exists := false
	if w.island != "" {
		_, err := os.Stat(filepath.Join(w.worldDir(), "WorldDescription.json"))
		exists = err == nil
	}
	if !exists && (w.island == "" || w.s.Option("ownWorldId") == "true") {
		w.island = strings.ToUpper(randomHex(16))
	}
	w.pers["WorldIslandId"] = w.island
	w.r5("R5LogCoopProxy", "Verbose", "UR5CoopIslandCollection::SetCurrentIslandId", "Set CurrentIslandId "+w.island, `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopIslandCollection.cpp:127`)
	if exists {
		// Carry on growing the world from where its last save left it.
		if b, err := os.ReadFile(filepath.Join(w.worldDir(), "MANIFEST-000001")); err == nil {
			fmt.Sscanf(string(b), "manifest %d", &w.generation)
		}
		return
	}
	os.MkdirAll(w.worldDir(), 0o755)
	desc := map[string]any{"Version": 1, "WorldDescription": map[string]any{
		"IslandId": w.island, "WorldName": "The Archipelago", "CreationTime": 6.391e+17, "WorldPresetType": "Medium",
		"WorldSettings": map[string]any{"BoolParameters": map[string]any{}, "FloatParameters": map[string]any{}, "TagParameters": map[string]any{}},
	}}
	b, _ := json.MarshalIndent(desc, "", "\t")
	os.WriteFile(filepath.Join(w.worldDir(), "WorldDescription.json"), b, 0o644)
	os.WriteFile(filepath.Join(w.worldDir(), "CURRENT"), []byte("MANIFEST-000001\n"), 0o644)
	w.writeDB()
	w.r5("R5LogCoopProxy", "", "UR5CoopIslandCollection::CreateWorld", "World description created. IslandId "+w.island, `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopIslandCollection.cpp:210`)
	w.backup()
}

// writeDB rewrites the world's database files; every save makes them a
// little bigger, as a played world grows.
func (w *wrServer) writeDB() {
	w.generation++
	os.WriteFile(filepath.Join(w.worldDir(), "MANIFEST-000001"), []byte(fmt.Sprintf("manifest %d\n", w.generation)), 0o644)
	os.WriteFile(filepath.Join(w.worldDir(), "000004.sst"), bytes.Repeat([]byte{byte(w.generation)}, 2000+150*w.generation), 0o644)
}

// backup writes the world's database and the server's own backup of it,
// the newest ending in _Latest.zip, keeping the last five.
func (w *wrServer) backup() {
	id := "Worlds/" + w.island
	w.r5("R5LogCoopProxy", "", "UR5CoopDocumentBackup::SaveBackup", id+". Save backup requested ...", `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopDocumentBackup.cpp:300`)
	w.writeDB()
	dir := w.backupDir()
	os.MkdirAll(dir, 0o755)
	entries, _ := os.ReadDir(dir)
	var old []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, "_Latest.zip") {
			plain := strings.TrimSuffix(name, "_Latest.zip") + ".zip"
			os.Rename(filepath.Join(dir, name), filepath.Join(dir, plain))
			name = plain
		}
		if strings.HasSuffix(name, ".zip") {
			old = append(old, name)
		}
	}
	sort.Strings(old)
	for len(old) > 4 {
		os.Remove(filepath.Join(dir, old[0]))
		old = old[1:]
	}
	stamp := time.Now().UTC().Format("2006.01.02-15.04.05.000")
	name := fmt.Sprintf("%s_%s_%s_Latest.zip", w.island, w.s.Version(), stamp)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("000004.sst")
	f.Write(bytes.Repeat([]byte{byte(w.generation)}, 2000+150*w.generation))
	zw.Close()
	os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644)
	w.dirty = false
	w.r5("R5LogCoopProxy", "", "UR5CoopDocumentBackup::SaveBackup", id+". Save backup has finished successfully", `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopDocumentBackup.cpp:320`)
}

// accountID is a player's account: the same for the same name, as a
// player's is the same every time they join.
func accountID(name string) string {
	sum := md5.Sum([]byte("windrose account " + name))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (w *wrServer) find(name string) int {
	for i, a := range w.connected {
		if a.name == name {
			return i
		}
	}
	return -1
}

// accounts is the server's list of accounts, as it writes it below a
// message: "\nConnected Accounts\n    1. …\nReserved Accounts\nDisconnected Accounts\n".
func (w *wrServer) accounts() string {
	var b strings.Builder
	row := func(i int, a *wrAccount) {
		now := time.Now()
		ready := "+00:00:00.000"
		if !a.ready.IsZero() {
			ready = dur(a.ready.Sub(a.joined))
		}
		end := now
		if !a.left.IsZero() {
			end = a.left
		}
		fmt.Fprintf(&b, "    %2d. Name '%s'. AccountId '%s'. State '%s'. NetAddress 'R5:%s'. UePortal ''. ReserveMoment %s. Connected in +00:00:01.500. IsReady in %s. TimeInGame %s. TimeOnServer %s. FarewellReason %s\n",
			i+1, a.name, a.id, a.state, a.session, a.joined.UTC().Format("2006.01.02-15.04.05"), ready, dur(end.Sub(a.ready)), dur(end.Sub(a.joined)), a.farewell)
	}
	b.WriteString("\nConnected Accounts\n")
	for i, a := range w.connected {
		row(i, a)
	}
	b.WriteString("Reserved Accounts\nDisconnected Accounts\n")
	for i, a := range w.disconnected {
		row(i, a)
	}
	return b.String()
}

// dur is a time span as the server writes it: +HH:MM:SS.mmm.
func dur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("+%02d:%02d:%02d.%03d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, int(d.Milliseconds())%1000)
}

func (w *wrServer) step(st Step) bool {
	dumpsOnly := w.s.Option("dumpsOnly") == "true"
	switch st.Verb {
	case "join":
		if w.find(st.Name) >= 0 || !w.s.Join(st.Name) {
			return true
		}
		a := &wrAccount{name: st.Name, id: accountID(st.Name), session: randomHex(16), state: "UeLoggedIn", joined: time.Now()}
		w.connected = append(w.connected, a)
		w.dirty = true
		if dumpsOnly {
			w.dump()
			return true
		}
		w.line("LogNet", "", "Login request: ?BLPlayerSessionId="+a.session+"?Name="+randomHex(16)+" userId: NULL:"+randomHex(16)+" platform: NULL")
		w.r5("R5LogDataKeeper", "", "UR5DataKeeperForServerCoop::OnAccountUePrelogin", "Coop. Ue prelogin. Options '?BLPlayerSessionId="+a.session+"?SplitscreenCount=1'. Address 'R5:"+a.session+"'", `Source\R5DataKeepers\Private\R5DataKeeperForServerCoop.cpp:304`)
		w.line("LogNet", "", "Join succeeded: "+a.name)
		w.r5("R5LogDataKeeper", "", "IR5DataKeeperStateLogger::LogSetState", "ServerAccount. AccountName '"+a.name+"'. AccountId "+a.id+". PlayerState BP_R5PlayerState_C_2147417529. Change state UeLoggedIn => WaitingForClientIsReady in 2.000 ms", `Source\R5DataKeepers\Private\R5DataKeeperStateLogger.cpp:40`)
		a.state = "WaitingForClientIsReady"
		w.r5("R5LogDataKeeper", "", "...erForServer::OnPlayerStateReplicateAccountId", "Account connected. AccountId "+a.id+w.accounts(), `Source\R5DataKeepers\Private\R5DataKeeperForServer.cpp:785`)
		a.state, a.ready = "ReadyToPlay", time.Now()
		w.r5("R5LogDataKeeper", "", "UR5DataKeeperForServer_Account::OnClientIsReady", "Client id ReadyToPlay. AccountId "+a.id, `Source\R5DataKeepers\Private\R5DataKeeperForServer_Account.cpp:612`)
	case "leave":
		i := w.find(st.Name)
		if i < 0 {
			return true
		}
		w.s.Leave(st.Name)
		a := w.connected[i]
		w.connected = append(w.connected[:i], w.connected[i+1:]...)
		a.left, a.farewell = time.Now(), "Go to lobby"
		w.disconnected = append(w.disconnected, a)
		w.dirty = true
		if dumpsOnly {
			w.dump()
			return true
		}
		w.r5("R5LogCoopProxy", "", "UR5CoopProxyServer::OnAccountDisconnected", "Account disconnected. Inform Cm. AccountId "+a.id+". BLPlayerSessionId "+a.session+". DisconnectReason 'BL disconnected'. FarewellReason 'Go to lobby'", `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:613`)
		w.r5("R5LogDataKeeper", "", "...rver::MoveAccountToListOfDisconnected", "Account disconnected. AccountId "+a.id+w.accounts(), `Source\R5DataKeepers\Private\R5DataKeeperForServer.cpp:830`)
	case "deny":
		w.r5("R5LogDataKeeper", "Warning", "...ataKeeperForServerCoop::OnAccountBLConnected", "Client sent wrong password. Disconnect it. ClientPassword guess. ServerPassword "+w.str("Password"), `Source\R5DataKeepers\Private\R5DataKeeperForServerCoop.cpp:270`)
	case "save":
		w.backup()
	case "count":
		w.dump()
	default:
		return false
	}
	return true
}

// dump writes the list of accounts below a message of its own, as the
// server does from time to time.
func (w *wrServer) dump() {
	w.r5("R5LogDataKeeper", "", "...KeeperForServer::TryToStartTerrainGeneration", "Server is ready to continue initialization"+w.accounts(), `Source\R5DataKeepers\Private\R5DataKeeperForServer.cpp:785`)
}

// interrupted is Ctrl+C: Unreal asks the engine to exit, and Windrose
// writes its exit backup on the way out. A second one quits at once.
func (w *wrServer) interrupted() {
	if w.stopping {
		w.line("LogCore", "Warning", "*** INTERRUPTED *** : FORCE QUIT")
		w.s.Exit(1)
		return
	}
	w.stopping = true
	w.line("LogCore", "Warning", "*** INTERRUPTED *** : CTRL-C TO FORCE QUIT")
	w.line("LogCore", "", "Engine exit requested (reason: ConsoleCtrl RequestExit)")
	w.s.Sleep(500 * time.Millisecond)
	w.r5("R5LogCoopProxy", "", "UR5CoopProxyServer::PreDeinitialize", "Start on exit sync backups", `Source\BackendProxy\R5CoopProxy\Private\R5CoopProxyServer.cpp:700`)
	w.r5("R5LogCoopProxy", "", "UR5CoopDocumentBackup::SyncBackups", "Start sync backups. Id Worlds/"+w.island+". IsInGameThread true", `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopDocumentBackup.cpp:350`)
	w.backup()
	w.r5("R5LogCoopProxy", "", "UR5CoopDocumentBackup::SyncBackups", "Finished sync backups. Id Worlds/"+w.island+". Result true", `Source\BackendProxy\R5CoopProxy\Private\Data\R5CoopDocumentBackup.cpp:360`)
	w.saveDescription()
	w.r5("R5LogDataKeeper", "", "UR5DataKeeperForServer::PreDeinitialize", "PreDeinitialize Coop"+w.accounts(), `Source\R5DataKeepers\Private\R5DataKeeperForServer.cpp:900`)
	w.line("LogExit", "", "Exiting.")
	w.s.Log(w.prefix() + "Log file closed, " + time.Now().UTC().Format("01/02/06 15:04:05"))
	w.s.Exit(0)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
