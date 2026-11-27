// Adding a world to a game, on its Worlds tab: drop a world (its folder, its
// files or a zip) on the page, or choose it, and it goes into that game's
// worlds. The panel only asks when the game already has a world with the
// same name. "From your own game" copies a world from the owner's own copy
// of the game on this computer. Each instance has its own upload, so two
// games never mix up their files.

import { h, icon, api, toast, openModal, confirmDialog, fmtBytes, ago } from './dom.js';
import { moduleFor } from './games.js';
import { enc, gameByID } from './state.js';

const ups = new Map(); // instance -> its upload
let appRef = null;

function upFor(id) {
  let up = ups.get(id);
  if (!up) {
    up = { phase: 'idle', label: '', total: 0, sent: 0, id: null, xhr: null, cands: [], choice: {}, job: 0, waitingFor: '', result: '', names: [], error: '' };
    ups.set(id, up);
  }
  return up;
}

const busy = (up) => !['idle', 'done', 'error'].includes(up.phase);
const redraw = () => { if (appRef) appRef.render(); };
const base = (gid) => `/api/games/${enc(gid)}`;

// recent are the worlds just added, by instance, so their cards glow once.
export const recent = new Map();

export function initImport(app) {
  appRef = app;
  const A = app.actions;
  A['upload-pick'] = (d) => { const el = document.getElementById(d.input); if (el) el.click(); };
  A['upload-chosen'] = (d, el) => {
    const files = [...el.files].map((f) => ({ file: f, path: f.webkitRelativePath || f.name }));
    el.value = ''; // so choosing the same thing again still works
    start(d.id, files);
  };
  A['upload-decide'] = (d) => decide(d.id, d.cand, d.choice);
  A['upload-cancel'] = (d) => cancel(d.id);
  A['upload-reset'] = (d) => reset(d.id);
  A['world-local'] = (d) => localImport(app, d.id);
}

// ---------- dropping files on the Worlds tab ----------

let dropFor = null; // the instance whose Worlds tab is open
let overlay = null;
let depth = 0;
const hasFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes('Files');
function show(on) {
  if (overlay) overlay.hidden = !on;
  document.body.classList.toggle('dragging-files', on);
}
function onEnter(e) { if (hasFiles(e) && dropFor) { e.preventDefault(); depth++; show(true); } }
function onOver(e) {
  if (!hasFiles(e) || !dropFor) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = busy(upFor(dropFor)) ? 'none' : 'copy';
}
function onLeave(e) { if (hasFiles(e) && dropFor) { depth = Math.max(0, depth - 1); if (!depth) show(false); } }
async function onDrop(e) {
  if (!hasFiles(e) || !dropFor) return;
  e.preventDefault();
  depth = 0;
  show(false);
  const id = dropFor;
  start(id, await droppedFiles(e.dataTransfer));
}

// listenForDrops lets files be dropped while a game's Worlds tab is open;
// elsewhere a dropped file does nothing (it would be unclear which game it
// is for).
export function listenForDrops(id, on) {
  if (!overlay) {
    overlay = h('div', { id: 'drop-overlay', hidden: true, 'aria-hidden': 'true' },
      h('div', null, icon('upload'), h('b', null, 'Drop to add the world'), h('span', { 'data-drop-what': '' }, 'A world folder, its files, or a .zip')));
    document.body.append(overlay);
    // Without these the browser would open a dropped file in place of the panel.
    window.addEventListener('dragenter', onEnter);
    window.addEventListener('dragover', onOver);
    window.addEventListener('dragleave', onLeave);
    window.addEventListener('drop', onDrop);
  }
  dropFor = on ? id : dropFor === id ? null : dropFor;
  const g = appRef && gameByID(appRef.status, id);
  const what = overlay.querySelector('[data-drop-what]');
  if (on && g && what) what.textContent = `For ${g.short}: a world folder, its files, or a .zip`;
}

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
  return tops.size === 1 ? `“${[...tops][0]}”` : `${files.length} files`;
}

async function start(gid, files) {
  if (!files || !files.length) return;
  const up = upFor(gid);
  if (busy(up)) {
    toast(`Please wait until ${up.label} has been added.`, 'warn');
    return;
  }
  Object.assign(up, { phase: 'uploading', label: describe(files), total: files.reduce((n, f) => n + f.file.size, 0), sent: 0, id: null, cands: [], choice: {}, job: 0, result: '', names: [], error: '' });
  redraw();
  let last = 0;
  try {
    up.id = (await api(`${base(gid)}/uploads`, { method: 'POST', body: {} })).id;
    await sendFiles(gid, up, files, (sent) => {
      up.sent = sent;
      if (Date.now() - last > 150) { last = Date.now(); redraw(); }
    });
    if (up.phase !== 'uploading') return; // cancelled
    up.phase = 'scanning';
    redraw();
    up.cands = (await api(`${base(gid)}/uploads/${enc(up.id)}/scan`, { method: 'POST', body: {} })).candidates || [];
  } catch (e) {
    if (up.phase === 'idle') return; // cancelled
    return fail(gid, e.message);
  }
  for (const c of up.cands) {
    if (c.problem || c.superseded || !c.name) up.choice[c.id] = 'skip';
    else if (!c.exists) up.choice[c.id] = 'import';
  }
  if (!up.cands.some((c) => !c.problem && !c.superseded && c.name)) {
    const c = up.cands[0];
    return fail(gid, c && c.problem ? c.problem : 'No world was found in what was added. Add the world’s folder, its files, or a zip of them.');
  }
  next(gid);
}

const undecided = (up) => up.cands.filter((c) => !up.choice[c.id]);

function next(gid) {
  const up = upFor(gid);
  if (undecided(up).length) {
    up.phase = 'deciding';
    redraw();
    return;
  }
  importNow(gid);
}

async function decide(gid, cid, choice) {
  const up = upFor(gid);
  const c = up.cands.find((x) => x.id === cid);
  if (!c || up.phase !== 'deciding') return;
  if (choice === 'replace' && c.inUse) {
    const g = gameByID(appRef.status, gid);
    const n = g ? Math.max(0, g.count) : 0;
    const ok = await confirmDialog({
      title: `Replace “${c.name}” now?`,
      message: `${g ? g.short : 'The server'} is playing this world. It saves and stops, swaps in the copy you added, and starts again.${n ? ` The ${n} player${n === 1 ? '' : 's'} online will be disconnected.` : ''} The current copy is kept as a backup.`,
      confirm: 'Replace and restart', icon: 'refresh',
    });
    if (!ok) return;
  }
  up.choice[cid] = choice;
  next(gid);
}

const retry = new Map();
async function importNow(gid) {
  const up = upFor(gid);
  clearTimeout(retry.get(gid));
  const choices = up.cands.map((c) => {
    const ch = up.choice[c.id];
    if (ch === 'keep') return { id: c.id, action: 'import', name: c.freeName };
    return { id: c.id, action: ch || 'skip' };
  });
  up.names = choices.filter((c) => c.action !== 'skip').map((c) => c.name || up.cands.find((x) => x.id === c.id).name);
  if (!up.names.length) {
    api(`${base(gid)}/uploads/${enc(up.id)}`, { method: 'DELETE' }).catch(() => {});
    toast('Nothing was added.', 'info');
    return reset(gid);
  }
  try {
    const res = await api(`${base(gid)}/uploads/${enc(up.id)}/import`, { method: 'POST', body: { choices } });
    up.job = res.job;
    up.phase = 'importing';
    redraw();
    appRef.poll();
  } catch (e) {
    if (e.status === 409) { // another job is under way: try again shortly
      up.phase = 'waiting';
      up.waitingFor = e.message;
      redraw();
      retry.set(gid, setTimeout(() => importNow(gid), 2000));
      return;
    }
    fail(gid, e.message);
  }
}

// importStatus follows the import job in the regular status updates.
export function importStatus(g) {
  const up = ups.get(g.id);
  if (!up || up.phase !== 'importing') return;
  const job = g.job;
  if (!job || job.id < up.job || (job.id === up.job && !job.finished)) return;
  if (job.id === up.job && job.error) return fail(g.id, job.error);
  up.phase = 'done';
  up.result = job.id === up.job ? job.result : '';
  recent.set(g.id, { names: up.names.slice(), until: Date.now() + 12000, scrolled: false });
}

// ownsImport tells the shell not to repeat this section's result as a
// message.
export function ownsImport(job) {
  for (const up of ups.values()) if (up.job && job.id === up.job) return true;
  return false;
}

function fail(gid, msg) {
  const up = upFor(gid);
  up.phase = 'error';
  up.error = msg;
  if (up.id) api(`${base(gid)}/uploads/${enc(up.id)}`, { method: 'DELETE' }).catch(() => {});
  up.id = null;
  redraw();
}

function cancel(gid) {
  const up = upFor(gid);
  clearTimeout(retry.get(gid));
  if (up.xhr) up.xhr.abort();
  if (up.id) api(`${base(gid)}/uploads/${enc(up.id)}`, { method: 'DELETE' }).catch(() => {});
  reset(gid);
}

function reset(gid) {
  clearTimeout(retry.get(gid));
  Object.assign(upFor(gid), { phase: 'idle', id: null, xhr: null, cands: [], choice: {}, job: 0, error: '', result: '' });
  redraw();
}

function sendFiles(gid, up, files, onProgress) {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    for (const f of files) fd.append('file', f.file, encodeURIComponent(f.path).replace(/%2F/gi, '/'));
    const xhr = new XMLHttpRequest();
    up.xhr = xhr;
    xhr.open('POST', `${base(gid)}/uploads/${enc(up.id)}/files`);
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

function head(ic, title, sub) {
  return h('div', { class: 'add-world-head' },
    h('div', { class: 'add-world-icon' }, icon(ic)),
    h('div', { class: 'grow' }, h('h2', null, title), sub ? h('p', { class: 'text-2' }, sub) : null));
}

function progressBar(pct) {
  const known = pct != null && pct >= 0;
  return h('div', { class: `progress${known ? '' : ' indeterminate'}` }, h('i', { style: { width: (known ? pct : 35) + '%' } }));
}

function clashCard(gid, c) {
  const yours = [c.modified ? `saved ${ago(c.modified)}` : '', fmtBytes(c.size)].filter(Boolean).join(' · ');
  const theirs = [c.serverModified ? `saved ${ago(c.serverModified)}` : '', c.serverSize ? fmtBytes(c.serverSize) : ''].filter(Boolean).join(' · ');
  const newer = c.modified && c.serverModified && new Date(c.modified) > new Date(c.serverModified);
  const btn = (choice, cls, ic, label) => h('button', { class: `btn btn-sm ${cls}`, 'data-action': 'upload-decide', 'data-id': gid, 'data-cand': c.id, 'data-choice': choice }, ic ? icon(ic) : null, label);
  return h('div', { class: 'callout warn clash', 'data-key': 'clash:' + c.id }, icon('alert'),
    h('div', { class: 'body' },
      h('p', null, h('strong', null, `There’s already a world called “${c.name}”.`)),
      h('p', { class: 'small text-2' }, `The one you added: ${yours}. The one here: ${theirs}.`, newer ? ' Yours is newer.' : ''),
      c.inUse ? h('p', { class: 'small text-2' }, 'The server is playing it right now. Replacing it restarts the server with your copy.') : null,
      h('div', { class: 'actions' },
        btn('replace', 'btn-primary', 'refresh', 'Replace the copy here'),
        c.freeName ? btn('keep', '', 'plus', `Keep both (add as “${c.freeName}”)`) : null,
        btn('skip', 'btn-ghost', null, 'Don’t add it')),
      h('p', { class: 'small muted', style: { marginTop: '8px' } }, 'Replacing keeps a backup of the copy here first, so it can be undone from the world’s Backups.')));
}

// importSection is the Add a world card on a game's Worlds tab.
export function importSection(app, g, data) {
  const up = upFor(g.id);
  const mod = moduleFor(g.game);
  const gid = g.id;
  const pick = `pick-${gid}`.replace(/[^\w-]/g, '_');
  const help = mod.worldsHelp ? h('details', { class: 'where' }, h('summary', null, 'Where do I find a world?'), h('p', { class: 'small text-2' }, mod.worldsHelp)) : null;
  const ownGame = h('button', { class: 'btn', 'data-action': 'world-local', 'data-id': gid }, icon('folder'), 'From your own game');
  let body;
  switch (up.phase) {
    case 'uploading': {
      const pct = up.total ? Math.min(100, (up.sent / up.total) * 100) : -1;
      body = [head('upload', `Adding ${up.label}…`, `Copying ${fmtBytes(up.sent)} of ${fmtBytes(up.total)}`), progressBar(pct),
        h('div', { class: 'row add-world-actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'upload-cancel', 'data-id': gid }, 'Cancel'))];
      break;
    }
    case 'scanning':
      body = [head('upload', `Adding ${up.label}…`, 'Looking for worlds…'), progressBar(-1)];
      break;
    case 'deciding': {
      const clashes = undecided(up);
      body = [head('globe', clashes.length === 1 ? 'One question first' : `${clashes.length} questions first`, null),
        clashes.map((c) => clashCard(gid, c)),
        h('div', { class: 'row add-world-actions' }, h('button', { class: 'btn btn-sm btn-ghost', 'data-action': 'upload-cancel', 'data-id': gid }, 'Cancel'))];
      break;
    }
    case 'waiting':
      body = [head('clock', `Adding ${up.label}…`, up.waitingFor), progressBar(-1),
        h('div', { class: 'row add-world-actions' }, h('button', { class: 'btn btn-sm btn-ghost', 'data-action': 'upload-cancel', 'data-id': gid }, 'Cancel'))];
      break;
    case 'importing': {
      const job = g.job && g.job.id === up.job ? g.job : null;
      body = [head('upload', job ? job.title + '…' : `Adding ${up.label}…`, job && job.message ? job.message : 'Putting it with the worlds…'), progressBar(-1)];
      break;
    }
    case 'done': {
      const one = up.names.length === 1 ? up.names[0] : null;
      const running = g.state === 'online' || g.state === 'starting';
      body = [h('div', { class: 'callout good' }, icon('check'), h('div', { class: 'body' },
        h('p', null, h('strong', null, up.result || (one ? `“${one}” was added.` : 'The worlds were added.'))),
        h('div', { class: 'actions' },
          one && g.runningWorld !== one ? h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'world-play', 'data-id': gid, 'data-name': one }, icon('play'), running ? 'Switch to this world' : 'Play this world') : null,
          h('button', { class: 'btn btn-sm', 'data-action': 'upload-reset', 'data-id': gid }, icon('plus'), 'Add another world'))))];
      break;
    }
    case 'error':
      body = [h('div', { class: 'callout bad' }, icon('alert'), h('div', { class: 'body' },
        h('p', null, h('strong', null, 'That world couldn’t be added. ')), h('p', null, up.error),
        h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'upload-reset', 'data-id': gid }, 'Try again')))), help];
      break;
    default:
      body = [
        head('upload', 'Add a world', `Drop a world here (its folder, its files, or a .zip) and it’s added to ${g.short}’s worlds.`),
        h('div', { class: 'row add-world-actions' },
          h('button', { class: 'btn btn-primary', 'data-action': 'upload-pick', 'data-input': pick + '-files' }, icon('upload'), 'Choose a zip or files'),
          h('button', { class: 'btn', 'data-action': 'upload-pick', 'data-input': pick + '-folder' }, icon('folder'), 'Choose a world folder'),
          data && data.canImportOwn !== false ? ownGame : null),
        help,
      ];
  }
  // The file pickers come first, so redraws never replace one while it's open.
  return h('section', { class: `card add-world ${up.phase}`, 'aria-label': 'Add a world', 'data-key': 'add-world:' + gid },
    h('input', { type: 'file', id: pick + '-folder', webkitdirectory: '', multiple: true, hidden: true, 'data-action': 'upload-chosen', 'data-id': gid, 'data-keep': '', 'data-key': 'pick-folder' }),
    h('input', { type: 'file', id: pick + '-files', multiple: true, accept: mod.uploadAccept || '.zip', hidden: true, 'data-action': 'upload-chosen', 'data-id': gid, 'data-keep': '', 'data-key': 'pick-files' }),
    body);
}

// ---------- from the owner's own game ----------

// candidateRows lets the owner choose, for each world found, to import it,
// replace the one here, or skip it.
export function candidateRows(cands) {
  const state = cands.map((c) => ({ c, action: c.problem || c.superseded || c.exists ? 'skip' : 'import', name: (c.exists && c.freeName) || c.name || '' }));
  const els = state.map((s) => {
    const c = s.c;
    const nameInput = h('input', { class: 'input', value: s.name, maxlength: 64, disabled: !c.renamable && !!c.name, placeholder: 'Its name here', 'aria-label': 'Name' });
    nameInput.oninput = () => { s.name = nameInput.value; };
    const sel = h('select', { class: 'input', style: { maxWidth: '260px' }, 'aria-label': `What to do with ${c.name || 'this world'}` },
      c.problem ? null : h('option', { value: c.exists ? 'replace' : 'import', selected: s.action !== 'skip' }, c.exists ? 'Replace the world here' : 'Import'),
      c.exists && c.renamable && !c.problem ? h('option', { value: 'import' }, 'Import with another name') : null,
      h('option', { value: 'skip', selected: s.action === 'skip' }, 'Skip'));
    sel.onchange = () => { s.action = sel.value; };
    return h('div', { class: 'cand' },
      h('div', { class: 'row' },
        h('b', { style: { color: 'var(--text)', fontSize: '16px' } }, c.name || 'Unnamed world'),
        c.exists ? h('span', { class: 'badge warn' }, 'Name already used') : null,
        h('span', { class: 'spacer' }),
        h('span', { class: 'small muted' }, `${fmtBytes(c.size)}${c.modified ? ' · saved ' + ago(c.modified) : ''}`)),
      c.source ? h('div', { class: 'small muted' }, 'From: ' + c.source) : null,
      c.note ? h('div', { class: 'small text-2' }, c.note) : null,
      c.superseded ? h('div', { class: 'small text-2' }, 'Another copy of this world in the list is used instead, so this one is skipped.') : null,
      c.problem ? h('div', { class: 'small', style: { color: 'var(--bad-text)' } }, c.problem) : null,
      c.exists ? h('div', { class: 'small text-2' }, c.inUse ? 'The server is playing this world now: replacing it restarts the server. A backup of the current world is kept first.'
        : 'Replacing keeps a backup of the current world first.') : null,
      h('div', { class: 'row' }, sel, c.renamable || !c.name ? nameInput : null));
  });
  return { el: els, choices: () => state.map((s) => ({ id: s.c.id, action: s.action, name: s.action === 'replace' ? '' : s.name.trim() })) };
}

async function localImport(app, gid) {
  const g = gameByID(app.status, gid);
  if (!g) return;
  let cands;
  try {
    cands = (await api(`${base(gid)}/local-worlds`)).candidates || [];
  } catch (e) {
    toast(e.message, 'bad', 9000);
    return;
  }
  if (!cands.length) {
    openModal({
      title: 'From your own game',
      body: [h('p', null, `No ${g.short} worlds were found in this computer’s own game saves.`),
        h('p', { class: 'small' }, 'To bring a world from another computer, copy it here (a USB stick, a chat or a cloud drive) and drop it on Add a world.')],
      actions: [{ label: 'Close', kind: 'primary' }],
    });
    return;
  }
  const rows = candidateRows(cands);
  openModal({
    title: 'From your own game',
    wide: true,
    body: [h('p', null, `These ${g.short} worlds were found on this computer. They’re copied, so the originals stay where they are.`), ...rows.el],
    actions: [{ label: 'Cancel' }, {
      label: 'Import', kind: 'primary', icon: 'check', onClick: async () => {
        await api(`${base(gid)}/local-worlds/import`, { method: 'POST', body: { choices: rows.choices() } });
        toast('Copying the worlds…', 'info');
        app.poll();
      },
    }],
  });
}
