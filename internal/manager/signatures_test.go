package manager

import (
	"context"
	"io"
	"time"

	"valheim-easy-server/internal/game"
	"valheim-easy-server/internal/platform"
	"valheim-easy-server/internal/records"
	"valheim-easy-server/internal/servers"
	"valheim-easy-server/internal/stats"
	"valheim-easy-server/internal/waker"
	"valheim-easy-server/internal/worlds"
)

// These pin the phase 0' contract (docs/dev/multigame/p0r3-decisions.md):
// the R3 streams call each other's functions, and the API stream calls the
// public methods, while each is still being written. A stream may fill in
// any body, and add private helpers in its own files freely; changing one
// of these signatures needs the lead, and must change this file too. The
// comment on each group names the stream whose file holds it.
var (
	// The public methods the new HTTP routes call (spec 6.2); M3's unless
	// the comment says otherwise.
	_ func(*Manager, string, StartOptions) error                   = (*Manager).StartGame
	_ func(*Manager, string) error                                 = (*Manager).StopGame
	_ func(*Manager, string) error                                 = (*Manager).RestartGame
	_ func(*Manager, string) error                                 = (*Manager).ForceStopGame
	_ func(*Manager, string, bool) error                           = (*Manager).InstallGame
	_ func(*Manager, string) error                                 = (*Manager).CancelGameJob
	_ func(*Manager, string) error                                 = (*Manager).StopAll
	_ func(*Manager, string, bool) error                           = (*Manager).SetStar
	_ func(*Manager, bool, bool) error                             = (*Manager).SetSidebar
	_ func(*Manager, string) (GameSettingsView, error)             = (*Manager).GameSettings
	_ func(*Manager, string, GameSettingsChange) error             = (*Manager).UpdateGameSettings
	_ func(*Manager, AppSettingsChange) error                      = (*Manager).UpdateAppSettings
	_ func(*Manager, string) (GameWorldsView, error)               = (*Manager).WorldsGame
	_ func(*Manager, string, string, bool) error                   = (*Manager).SelectWorldGame
	_ func(*Manager, string, string, game.Values, bool) error      = (*Manager).CreateWorldGame
	_ func(*Manager, string, string, game.Values) error            = (*Manager).SetWorldValuesGame
	_ func(*Manager, string, string) error                         = (*Manager).DeleteWorldGame
	_ func(*Manager, string, string) error                         = (*Manager).BackupWorldGame
	_ func(*Manager, string, string, string) error                 = (*Manager).RestoreWorldGame
	_ func(*Manager, string, string, string) error                 = (*Manager).RenameWorldGame
	_ func(*Manager, string, string) (string, string, error)       = (*Manager).PrepareDownloadGame
	_ func(*Manager, string, io.Writer, string, string) error      = (*Manager).ExportBackupGame
	_ func(*Manager, string) (*worlds.Upload, error)               = (*Manager).NewUploadGame
	_ func(*Manager, string, string) (*worlds.Upload, error)       = (*Manager).UploadGame
	_ func(*Manager, string, string) ([]worlds.Candidate, error)   = (*Manager).FinishUploadGame
	_ func(*Manager, string, string) error                         = (*Manager).DiscardUploadGame
	_ func(*Manager, string, string, []worlds.Choice) (int, error) = (*Manager).ImportUploadGame
	_ func(*Manager, string) ([]worlds.Candidate, error)           = (*Manager).LocalWorldsGame
	_ func(*Manager, string, []worlds.Choice) (int, error)         = (*Manager).ImportLocalGame
	_ func(*Manager, string) ([]KnownPlayer, error)                = (*Manager).KnownPlayersGame
	_ func(*Manager, string) ([]GameList, error)                   = (*Manager).ListsGame
	_ func(*Manager, string, string, []string) error               = (*Manager).SetListGame
	_ func(*Manager, string, string) error                         = (*Manager).AllowPlayer
	_ func(*Manager, string, int64, int) ([]LogLine, int64, error) = (*Manager).LogsGame
	_ func(*Manager, string) (string, error)                       = (*Manager).CurrentLogPathGame
	_ func(*Manager) []CatalogGame                                 = (*Manager).Catalog         // A
	_ func(*Manager, string) ([]worlds.Candidate, error)           = (*Manager).CatalogLocal    // A
	_ func(*Manager, AddGameRequest) (AddedGame, error)            = (*Manager).AddGame         // A
	_ func(*Manager, string) (int, error)                          = (*Manager).RemoveGame      // A
	_ func(*Manager, string) (map[string]int, error)               = (*Manager).PickPorts       // A
	_ func(*Manager, string, time.Time) ([]stats.Sample, error)    = (*Manager).HistoryGame     // A
	_ func(*Manager, string) (stats.Summary, error)                = (*Manager).ByPlayersGame   // A
	_ func(*Manager, string) error                                 = (*Manager).ResetStatsGame  // A
	_ func(*Manager, string) error                                 = (*Manager).KeepOn          // D
	_ func(*Manager, string, string) (records.Summary, error)      = (*Manager).RecordsGame     // D
	_ func(*Manager, string) error                                 = (*Manager).SetMainDiscord  // D
	_ func(*Manager, string, string) error                         = (*Manager).TestGameDiscord // D
	_ func(*Manager) RouterView                                    = (*Manager).Router          // N
	_ func(*Manager) error                                         = (*Manager).CheckRouter     // N
	_ func(*Manager) error                                         = (*Manager).FixFirewall     // A

	// What the streams call of each other's (spec 9.3).
	_ func(*Manager, *instance, GameDiscordChange) error                                    = (*Manager).setGameDiscord         // D
	_ func(*Manager, *instance) GameDiscordSettings                                         = (*Manager).gameDiscordLocked      // D
	_ func(*Manager, *instance) string                                                      = (*Manager).gameLinkLocked         // D
	_ func(*Manager, *instance) *GameDiscordStatus                                          = (*Manager).discordStatusLocked    // D
	_ func(*Manager, *instance) *AskStatus                                                  = (*Manager).askStatusLocked        // D
	_ func(*Manager, *instance) *AutoStopStatus                                             = (*Manager).autoStopStatusLocked   // A
	_ func(*Manager, *instance) int                                                         = (*Manager).autoStopMinutesLocked  // A
	_ func(*Manager, *instance, stopReason) error                                           = (*Manager).autoStopGame           // A
	_ func(int) string                                                                      = autoStopText                      // A
	_ func(*Manager, *instance) error                                                       = (*Manager).memoryCheckLocked      // A
	_ func(*Manager) []RunningGame                                                          = (*Manager).runningGamesLocked     // A
	_ func(*Manager, game.Info, map[string]int) (map[string]int, error)                     = (*Manager).pickPortsLocked        // A
	_ func(*Manager) map[int]string                                                         = (*Manager).reservedPortsLocked    // A
	_ func(*Manager, *instance) error                                                       = (*Manager).checkPorts             // A
	_ func(*Manager, *instance, map[string]int) game.FieldErrors                            = (*Manager).checkChosenPortsLocked // A
	_ func(*Manager) []platform.GameRule                                                    = (*Manager).gameRulesLocked        // A
	_ func(*Manager)                                                                        = (*Manager).checkGameFirewalls     // A
	_ func(*Manager, string, string) error                                                  = (*Manager).startFromWake          // A
	_ func(*Manager)                                                                        = (*Manager).scheduleResume         // A
	_ func(*Manager)                                                                        = (*Manager).resumeNow              // A
	_ func(*Manager)                                                                        = (*Manager).cancelResumeLocked     // A
	_ func(*Manager, context.Context) bool                                                  = (*Manager).networkBack            // A
	_ func(*Manager) []waker.GameCommand                                                    = (*Manager).wakerCommandsLocked    // WK
	_ func(*Manager)                                                                        = (*Manager).starsChanged           // WK
	_ func(*Manager, *instance, error)                                                      = (*Manager).wakeReply              // WK
	_ func(*Manager, *instance) map[string]string                                           = (*Manager).routerStatusLocked     // N
	_ func(*Manager) *RouterStatus                                                          = (*Manager).routerSummaryLocked    // N
	_ func(*Manager, *instance)                                                             = (*Manager).unmapPorts             // N
	_ func(*Manager, bool)                                                                  = (*Manager).routerPortsChanged     // N
	_ func(*Manager, string)                                                                = (*Manager).noteNAT                // N
	_ func(*Manager, *instance, StartOptions) error                                         = (*Manager).startGameWith          // M3
	_ func(*Manager, string) (*instance, error)                                             = (*Manager).instanceByID           // M3
	_ func(*Manager, servers.Instance) (*instance, error)                                   = (*Manager).newGameInstance        // M3
	_ func(*Manager, *instance)                                                             = (*Manager).addInstanceLocked      // M3
	_ func(*Manager, string)                                                                = (*Manager).removeInstanceLocked   // M3
	_ func(*Manager, func(*servers.File) error) error                                       = (*Manager).updateServers          // M3
	_ func(*Manager) []string                                                               = (*Manager).starsLocked            // M3
	_ func(*Manager, *instance) bool                                                        = (*Manager).isStarredLocked        // M3
	_ func(*Manager, *instance) bool                                                        = (*Manager).isDisabledLocked       // M3
	_ func(*Manager, *instance) bool                                                        = (*Manager).isHiddenLocked         // M3
	_ func(*Manager, *instance) (game.Level, string)                                        = (*Manager).canRunLocked           // M3
	_ func(*Manager, *instance) bool                                                        = (*Manager).presenceProvenLocked   // M3
	_ func(*Manager, *instance, string) bool                                                = (*Manager).portChosenLocked       // M3
	_ func(*Manager, *instance, gameView) GameStatus                                        = (*Manager).gameStatusLocked       // M3
	_ func(*Manager, *instance, string, string, string)                                     = (*Manager).addGameActivity        // M3
	_ func(*Manager, *instance, string, string, func(context.Context, *jobCtl) error) error = (*Manager).runQuietJob            // M3
	_ func(*Manager, func(observer))                                                        = (*Manager).observeLocked          // M3
)
