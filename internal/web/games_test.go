package web

// The manager finds Valheim through the game registry, which main fills by
// importing internal/games/all. The web tests make their own manager, so
// they import it too.
import _ "valheim-easy-server/internal/games/all"
