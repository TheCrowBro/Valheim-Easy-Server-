// Add a world: drop a world (its folder, its .fwl and .db files, or a zip)
// anywhere in the window, or choose it, and it goes straight into the
// server's worlds folder. The panel only asks when the server already has a
// world with the same name.
//
// Moved from the old panel's upload.js. The steps, texts and choices are
// the same; what changed is where they go and where they live:
// - each instance has its own upload (U(game)), sent to that instance's
//   routes (/api/games/{id}/uploads/...);
// - the import job is followed in the instance's own entry of the status
//   (game.job), each time the Worlds tab is drawn;
// - a drop is taken while one of Valheim's pages is shown (the address is
//   #/game/<instance>/...), and opens that instance's Worlds tab, as a drop
//   on any page opened the Worlds page before.

import { h, icon, api, toast, confirmDialog, fmtBytes, ago } from '../../dom.js';
import { gameApi, perGame } from './common.js';

// The files a world can be: a zip, or Valheim's own world files.
export const ACCEPT = '.zip,.fwl,.db,.fwl2,.db2,.chunks,.chunk,.ok';

const U = perGame(() => ({
  phase: 'idle', // idle | uploading | scanning | deciding | waiting | importing | done | error
  label: '',     // what is being added, e.g. “Midgard” or “3 files”
  total: 0,
  sent: 0,
  id: null,      // the upload on the server
  xhr: null,
  cands: [],
  choice: {},    // candidate id -> import | replace | keep | skip
  job: 0,        // the import job
  waitingFor: '',
  result: '',
  names: [],     // worlds put on the server
  error: '',
  retry: null,   // the timer that tries the import again
  drawn: 0,      // when the progress was last drawn
}));

// recent are the worlds an import just put on the server, by instance, so
// the Worlds tab can show them.
export const recent = perGame(() => ({ names: [], until: 0, scrolled: false }));

const games = new Map(); // instance -> its latest entry in the status, from the Worlds tab
let redrawTab = () => {};
let playWorld = () => {};

// connect is called by the Worlds tab: how to redraw it, and how to play a
// world (the Play this world button when one world was added).
export function connect({ redraw, play }) {
  redrawTab = redraw;
  playWorld = play;
}

// noteGame keeps the instance's latest entry, for the drop and the replace
// question, which come between two draws.
export function noteGame(game) {
  games.set(game.id, game);
}

const busy = (u) => !['idle', 'done', 'error'].includes(u.phase);
const redraw = () => redrawTab();

// ---------- dropping files anywhere in the window ----------

// The Valheim instance whose page is shown, from the address, or null.
function shownGame() {
  const m = /^#\/game\/([^/?]+)/.exec(location.hash || '');
  return m ? games.get(decodeURIComponent(m[1])) || null : null;
}

let listening = false;
export function listenForDrops() {
  if (listening) return;
  listening = true;
  const overlay = h('div', { class: 'vh-drop', hidden: true, 'aria-hidden': 'true' },
    h('div', null, icon('upload'), h('b', null, 'Drop to add the world to the server'),
      h('span', null, 'A world folder, its .fwl and .db files, or a .zip')));
  document.body.append(overlay);
  const hasFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes('Files');
  let depth = 0;
  const show = (on) => {
    overlay.hidden = !on;
    document.body.classList.toggle('dragging-files', on);
  };
  // Without these the browser would open a dropped file in place of the
  // control panel.
  window.addEventListener('dragenter', (e) => {
    if (!hasFiles(e) || !shownGame()) return;
    e.preventDefault();
    depth++;
    show(true);
  });
  window.addEventListener('dragover', (e) => {
    const game = shownGame();
    if (!hasFiles(e) || !game) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = busy(U(game)) ? 'none' : 'copy';
  });
  window.addEventListener('dragleave', (e) => {
    if (!hasFiles(e) || !shownGame()) return;
    depth = Math.max(0, depth - 1);
    if (!depth) show(false);
  });
  window.addEventListener('drop', async (e) => {
    const game = shownGame();
    if (!hasFiles(e) || !game) return;
    e.preventDefault();
    depth = 0;
    show(false);
    const pending = droppedFiles(e.dataTransfer); // must start before the event ends
    const worlds = `#/game/${encodeURIComponent(game.id)}/worlds`;
    if (location.hash !== worlds) location.hash = worlds;
    start(game, await pending);
  });
}

// droppedFiles walks dropped folders so their structure is kept.
async function droppedFiles(dt) {
  const out = [];
  const entries = [...dt.items].map((i) => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
  if (!entries.length) return [...dt.files].map((f) => ({ file: f, path: f.name }));
  const walk = async (entry, prefix) => {
    if (entry.isFile) {
      const file = await new Promise((res, rej) => entry.file(res, rej));
      out.push({ file, path: prefix + file.name });
    } else if (entry.isDirectory) {
      const reader = entry.createReader();
      let batch;
      do {
        batch = await new Promise((res, rej) => reader.readEntries(res, rej));
        for (const child of batch) await walk(child, prefix + entry.name + '/');
      } while (batch.length);
    }
  };
  for (const entry of entries) await walk(entry, '');
  return out;
}

// ---------- the steps ----------

function describe(files) {
  const tops = new Set(files.map((f) => f.path.split('/')[0]));
  if (tops.size === 1) return `“${[...tops][0]}”`;
  return `${files.length} files`;
}

async function start(game, files) {
  if (!files || !files.length) return;
  const up = U(game);
  if (busy(up)) {
    toast(`Please wait until ${up.label} has been added.`, 'warn');
    return;
  }
  Object.assign(up, {
    phase: 'uploading', label: describe(files), total: files.reduce((n, f) => n + f.file.size, 0), sent: 0,
    id: null, cands: [], choice: {}, job: 0, result: '', names: [], error: '',
  });
  redraw();
  try {
    up.id = (await api(gameApi(game, '/uploads'), { method: 'POST', body: {} })).id;
    await sendFiles(game, up, files, (sent) => {
      up.sent = sent;
      if (Date.now() - up.drawn > 150) { up.drawn = Date.now(); redraw(); }
    });
    if (up.phase !== 'uploading') return; // cancelled
    up.phase = 'scanning';
    redraw();
    up.cands = (await api(gameApi(game, `/uploads/${up.id}/scan`), { method: 'POST', body: {} })).candidates;
  } catch (e) {
    if (up.phase === 'idle') return; // cancelled
    return fail(game, e.message);
  }
  for (const c of up.cands) {
    if (c.problem || c.superseded || !c.name) up.choice[c.id] = 'skip';
    else if (!c.exists) up.choice[c.id] = 'import';
  }
  const usable = up.cands.filter((c) => !c.problem && !c.superseded && c.name);
  if (!usable.length) {
    const c = up.cands[0];
    return fail(game, c && c.problem ? c.problem : 'That world’s files don’t say what it’s called. Put them in a folder named after the world and add the folder.');
  }
  next(game);
}

function undecided(up) {
  return up.cands.filter((c) => !up.choice[c.id]);
}

function next(game) {
  const up = U(game);
  if (undecided(up).length) {
    up.phase = 'deciding';
    redraw();
    return;
  }
  importNow(game);
}

async function decide(game, id, choice) {
  const up = U(game);
  const c = up.cands.find((x) => x.id === id);
  if (!c || up.phase !== 'deciding') return;
  if (choice === 'replace' && c.inUse) {
    const now = games.get(game.id) || game;
    const n = (now.players || []).length;
    const ok = await confirmDialog({
      title: `Replace “${c.name}” now?`,
      message: `The server is playing this world. It saves and stops, swaps in the copy you added, and starts again (about a minute).${n ? ` The ${n} player${n === 1 ? '' : 's'} online will be disconnected.` : ''} The server’s current copy is kept as a backup.`,
      confirm: 'Replace and restart', icon: 'refresh',
    });
    if (!ok) return;
  }
  up.choice[id] = choice;
  next(game);
}

async function importNow(game) {
  const up = U(game);
  clearTimeout(up.retry);
  const choices = up.cands.map((c) => {
    const ch = up.choice[c.id];
    if (ch === 'keep') return { id: c.id, action: 'import', name: c.freeName };
    return { id: c.id, action: ch || 'skip' };
  });
  up.names = choices.filter((c) => c.action !== 'skip').map((c) => c.name || up.cands.find((x) => x.id === c.id).name);
  if (!up.names.length) {
    api(gameApi(game, '/uploads/' + up.id), { method: 'DELETE' }).catch(() => {});
    toast('Nothing was added.', 'info');
    return reset(game);
  }
  try {
    const res = await api(gameApi(game, `/uploads/${up.id}/import`), { method: 'POST', body: { choices } });
    up.job = res.job;
    up.phase = 'importing';
    redraw();
  } catch (e) {
    if (e.status === 409) { // another job is running: try again shortly
      up.phase = 'waiting';
      up.waitingFor = e.message;
      redraw();
      up.retry = setTimeout(() => importNow(game), 2000);
      return;
    }
    fail(game, e.message);
  }
}

// uploadStatus follows the import job in the instance's entry of the status,
// each time the Worlds tab is drawn.
export function uploadStatus(game) {
  const up = U(game);
  const job = game.job;
  if (up.phase !== 'importing' || !job) return;
  if (job.id < up.job || (job.id === up.job && !job.finished)) return;
  if (job.id === up.job && job.error) return fail(game, job.error);
  // Finished (or a later job has already started, so this one is done).
  up.phase = 'done';
  up.result = job.id === up.job ? job.result : '';
  const r = recent(game);
  r.names = up.names.slice();
  r.until = Date.now() + 12000;
  r.scrolled = false;
}

// ownsJob says the Add a world section shows this job's outcome itself.
export function ownsJob(game, id) {
  const up = U(game);
  return id === up.job && up.job > 0;
}

function fail(game, msg) {
  const up = U(game);
  up.phase = 'error';
  up.error = msg;
  if (up.id) api(gameApi(game, '/uploads/' + up.id), { method: 'DELETE' }).catch(() => {});
  up.id = null;
  redraw();
}

function cancel(game) {
  const up = U(game);
  clearTimeout(up.retry);
  if (up.xhr) up.xhr.abort();
  if (up.id) api(gameApi(game, '/uploads/' + up.id), { method: 'DELETE' }).catch(() => {});
  reset(game);
}

function reset(game) {
  const up = U(game);
  clearTimeout(up.retry);
  Object.assign(up, { phase: 'idle', id: null, xhr: null, cands: [], choice: {}, job: 0, error: '', result: '' });
  redraw();
}

function sendFiles(game, up, files, onProgress) {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    for (const f of files) fd.append('file', f.file, encodeURIComponent(f.path).replace(/%2F/gi, '/'));
    const xhr = new XMLHttpRequest();
    up.xhr = xhr;
    xhr.open('POST', gameApi(game, `/uploads/${up.id}/files`));
    xhr.setRequestHeader('X-VES', '1');
    xhr.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(e.loaded); };
    xhr.onload = () => {
      up.xhr = null;
      let resp = {};
      try { resp = JSON.parse(xhr.responseText); } catch { /* not JSON */ }
      if (xhr.status >= 200 && xhr.status < 300) resolve(resp);
      else reject(new Error(resp.error || `Adding the files failed (${xhr.status}).`));
    };
    xhr.onerror = () => { up.xhr = null; reject(new Error('Copying the files was interrupted. Please try again.')); };
    xhr.onabort = () => { up.xhr = null; reject(new Error('Cancelled.')); };
    xhr.send(fd);
  });
}

// ---------- the section ----------

// WHERE is where Valheim keeps its worlds on the owner's own computers, in
// one line of plain text (the contract's worldsHelp).
export const WHERE = 'On a PC, Valheim keeps its worlds in %userprofile%\\AppData\\LocalLow\\IronGate\\Valheim\\worlds_local; '
  + 'on a Mac, in ~/Library/Application Support/IronGate/Valheim/worlds_local. In Valheim 1.0 each world is a folder there; '
  + 'older worlds are two files, Name.fwl and Name.db.';

export const whereHelp = () => [
  h('ol', { class: 'steps' },
    h('li', null, 'On the PC where the world was played, press ', h('kbd', null, 'Windows'), ' + ', h('kbd', null, 'R'), ', paste this and press Enter:',
      h('div', { class: 'mono small path-box' }, '%userprofile%\\AppData\\LocalLow\\IronGate\\Valheim\\worlds_local'),
      'In Valheim 1.0 each world is a folder there. Older worlds are two files, ', h('span', { class: 'mono' }, 'Name.fwl'), ' and ', h('span', { class: 'mono' }, 'Name.db'), '.'),
    h('li', null, 'Bring it to this computer: on a USB stick, or zip it and send it through a chat or cloud drive, then download it here.'),
    h('li', null, 'Drop the folder, the two files or the zip here.')),
  h('p', { class: 'small muted' }, 'Worlds kept in Steam Cloud must first be moved to local saves in Valheim (Manage saves). Played Valheim on this computer? Use ', h('b', null, 'Import from this PC'), '.'),
];

function pickers(game) {
  const chosen = (e) => {
    const el = e.target;
    const files = [...el.files].map((f) => ({ file: f, path: f.webkitRelativePath || f.name }));
    el.value = ''; // so choosing the same thing again still works
    start(games.get(game.id) || game, files);
  };
  return [
    h('input', { type: 'file', id: `vh-folder-${game.id}`, webkitdirectory: '', multiple: true, hidden: true, onchange: chosen, 'data-keep': '', 'data-key': 'pick-folder' }),
    h('input', { type: 'file', id: `vh-files-${game.id}`, multiple: true, accept: ACCEPT, hidden: true, onchange: chosen, 'data-keep': '', 'data-key': 'pick-files' }),
  ];
}

const pick = (id) => () => { const el = document.getElementById(id); if (el) el.click(); };

function head(ic, title, sub) {
  return h('div', { class: 'add-world-head' },
    h('div', { class: 'add-world-icon' }, icon(ic)),
    h('div', { class: 'grow' }, h('h2', null, title), sub ? h('p', { class: 'text-2' }, sub) : null));
}

function progressBar(pct) {
  const known = pct != null && pct >= 0;
  return h('div', { class: `progress${known ? '' : ' indeterminate'}` }, h('i', { style: { width: (known ? pct : 35) + '%' } }));
}

function clashCard(game, c) {
  const yours = [c.modified ? `saved ${ago(c.modified)}` : '', fmtBytes(c.size)].filter(Boolean).join(' · ');
  const theirs = [c.serverModified ? `saved ${ago(c.serverModified)}` : '', c.serverSize ? fmtBytes(c.serverSize) : ''].filter(Boolean).join(' · ');
  const newer = c.modified && c.serverModified && new Date(c.modified) > new Date(c.serverModified);
  const choose = (choice) => () => decide(game, c.id, choice);
  return h('div', { class: 'callout warn clash', 'data-key': 'clash:' + c.id }, icon('alert'),
    h('div', { class: 'body' },
      h('p', null, h('strong', null, `The server already has a world called “${c.name}”.`)),
      h('p', { class: 'small text-2' }, `The one you added: ${yours}. The server’s: ${theirs}.`,
        newer ? ' Yours is newer.' : ''),
      c.inUse ? h('p', { class: 'small text-2' }, 'The server is playing it right now. Replacing it restarts the server with your copy.') : null,
      h('div', { class: 'actions' },
        h('button', { class: 'btn btn-sm btn-primary', type: 'button', onclick: choose('replace') }, icon('refresh'), 'Replace the server’s copy'),
        c.freeName ? h('button', { class: 'btn btn-sm', type: 'button', onclick: choose('keep') }, icon('plus'), `Keep both (add as “${c.freeName}”)`) : null,
        h('button', { class: 'btn btn-sm btn-ghost', type: 'button', onclick: choose('skip') }, 'Don’t add it')),
      h('p', { class: 'small muted', style: { marginTop: '8px' } }, 'Replacing keeps a backup of the server’s copy first, so it can be undone from the world’s Backups.')));
}

export function uploadSection(game) {
  noteGame(game);
  const up = U(game);
  const cancelBtn = (ghost) => h('button', { class: `btn btn-sm${ghost ? ' btn-ghost' : ''}`, type: 'button', onclick: () => cancel(game) }, 'Cancel');
  const resetBtn = (ic, label) => h('button', { class: 'btn btn-sm', type: 'button', onclick: () => reset(game) }, ic ? icon(ic) : null, label);
  let body;
  switch (up.phase) {
    case 'uploading': {
      const pct = up.total ? Math.min(100, (up.sent / up.total) * 100) : -1;
      body = [
        head('upload', `Adding ${up.label}…`, `Copying ${fmtBytes(up.sent)} of ${fmtBytes(up.total)}`),
        progressBar(pct),
        h('div', { class: 'row add-world-actions' }, cancelBtn(false)),
      ];
      break;
    }
    case 'scanning':
      body = [head('upload', `Adding ${up.label}…`, 'Looking for worlds…'), progressBar(-1)];
      break;
    case 'deciding': {
      const clashes = undecided(up);
      const adding = up.cands.filter((c) => up.choice[c.id] === 'import' || up.choice[c.id] === 'keep' || up.choice[c.id] === 'replace');
      const skipped = up.cands.filter((c) => c.superseded).length;
      body = [
        head('globe', clashes.length === 1 ? 'One question first' : `${clashes.length} questions first`,
          adding.length ? `Ready to add: ${adding.map((c) => `“${up.choice[c.id] === 'keep' ? c.freeName : c.name}”`).join(', ')}.` : null),
        clashes.map((c) => clashCard(game, c)),
        skipped ? h('p', { class: 'small muted' }, `${skipped} other cop${skipped === 1 ? 'y' : 'ies'} of the same world${skipped === 1 ? '' : 's'}, such as Valheim’s own backups, will be skipped.`) : null,
        h('div', { class: 'row add-world-actions' }, cancelBtn(true)),
      ];
      break;
    }
    case 'waiting':
      body = [
        head('clock', `Adding ${up.label}…`, up.waitingFor.replace(/^Please wait: /, 'Waiting: ')),
        progressBar(-1),
        h('div', { class: 'row add-world-actions' }, cancelBtn(true)),
      ];
      break;
    case 'importing': {
      const job = game.job && game.job.id === up.job ? game.job : null;
      body = [head('upload', job ? job.title + '…' : `Adding ${up.label}…`, job && job.message ? job.message : 'Putting it in the worlds folder…'), progressBar(-1)];
      break;
    }
    case 'done': {
      const one = up.names.length === 1 ? up.names[0] : null;
      const running = game.state === 'online' || game.state === 'starting';
      const isRunning = one && game.runningWorld === one;
      body = [h('div', { class: 'callout good' }, icon('check'),
        h('div', { class: 'body' },
          h('p', null, h('strong', null, up.result || (one ? `“${one}” is on the server.` : 'The worlds are on the server.'))),
          h('div', { class: 'actions' },
            one && !isRunning ? h('button', { class: 'btn btn-sm btn-primary', type: 'button', onclick: () => playWorld(one) }, icon('play'), running ? 'Switch to this world' : 'Play this world') : null,
            !one ? h('span', { class: 'small text-2' }, 'Choose one below to play it.') : null,
            resetBtn('plus', 'Add another world'))))];
      break;
    }
    case 'error':
      body = [h('div', { class: 'callout bad' }, icon('alert'),
        h('div', { class: 'body' },
          h('p', null, h('strong', null, 'That world couldn’t be added. ')), h('p', null, up.error),
          h('div', { class: 'actions' }, resetBtn(null, 'Try again')))),
      h('details', { class: 'where' }, h('summary', null, 'Where do I find a world?'), whereHelp())];
      break;
    default:
      body = [
        head('upload', 'Add a world', 'Drop a world here: its folder, its .fwl and .db files, or a .zip. It goes into the server’s worlds folder by itself.'),
        h('div', { class: 'row add-world-actions' },
          h('button', { class: 'btn btn-primary', type: 'button', onclick: pick(`vh-folder-${game.id}`) }, icon('folder'), 'Choose a world folder'),
          h('button', { class: 'btn', type: 'button', onclick: pick(`vh-files-${game.id}`) }, icon('upload'), 'Choose a zip or files')),
        h('details', { class: 'where' }, h('summary', null, 'Where do I find a world?'), whereHelp()),
      ];
  }
  // The file pickers come first so refreshes never replace them while one is open.
  return h('section', { class: `card add-world ${up.phase}`, 'aria-label': 'Add a world', 'data-key': 'add-world:' + game.id }, pickers(game), body);
}
