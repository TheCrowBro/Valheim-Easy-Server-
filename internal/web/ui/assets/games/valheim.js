// Valheim's module for the control panel (games/README.md, spec 6.1): the
// parts of the panel only Valheim has, moved here from the old panel's
// pages so they keep working on Valheim's page in the new panel.
//
// - How friends join (Home): why there's no join code, the mods players
//   need with the mods code, and the Tutorial button (valheim/join.js).
// - Worlds: today's Worlds page, with each world's difficulty and mods,
//   adding a world by dropping or choosing it, importing worlds from this
//   computer, downloads and backups (valheim/worlds.js, upload.js).
// - Mods: each world's mods and what players need (valheim/mods.js).
// - Players: today's Players page, with Valheim's player IDs, Make admin
//   and Ban, and its admin / banned / allowed lists (valheim/players.js).
// - Settings: today's Valheim settings, plus what every game's Settings
//   tab has in the new panel (valheim/settings.js).
// - The Log tab's line styles and the activity feed's icons for Valheim's
//   own events.
//
// Home (state, Start and Stop, who's on, usage, the setup checklist),
// Stats and the Log itself are the shell's, the same for every game.
// The old panel's pages keep working until stream UI removes them; these
// files don't use them.

import { joinExtras, joinCard } from './valheim/join.js';
import { worldsTab, worldCardExtras } from './valheim/worlds.js';
import { modsTab } from './valheim/mods.js';
import { playersTab } from './valheim/players.js';
import { settingsTab } from './valheim/settings.js';
import { ACCEPT, WHERE } from './valheim/upload.js';
import { styles } from './valheim/common.js';

styles();

// logClass styles the Valheim server's own log lines as the old Log page
// did: errors stand out, players arriving and the join code are
// highlighted, and saves are marked.
function logClass(text) {
  if (/error|exception|failed|crash/i.test(text)) return 'warn';
  if (/Closing socket/.test(text)) return 'leave';
  if (/Got character ZDOID|join code|Opened (Steam|PlayFab) server/.test(text)) return 'join';
  if (/World save|World saved/.test(text)) return 'save';
  return null;
}

export default {
  id: 'valheim',
  joinExtras: (game) => joinExtras(game),
  // Not in the contract yet: the whole How friends join card as the old
  // Home drew it, for the shell to use in place of its own if the lead
  // adds the hook. Nothing calls it until then.
  joinCard: (game) => joinCard(game),
  logClass,
  // Valheim's own feed kinds, with the icons the old Home gave them.
  feedIcons: {
    wrong_password: ['key', 'warn'],
    bad_version: ['alert', 'warn'],
    load_failed: ['alert', 'bad'],
    bad_password_config: ['key', 'bad'],
  },
  worldsHelp: WHERE,
  uploadAccept: ACCEPT,
  worldCard: (world, game) => worldCardExtras(world, game),
  tabs: {
    worlds: worldsTab,
    players: playersTab,
    mods: modsTab,
    settings: settingsTab,
  },
};
