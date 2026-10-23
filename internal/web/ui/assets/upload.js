// Add a world: drop a world (its folder, its .fwl and .db files, or a zip)
// anywhere in the window, or choose it, and it goes straight into the
// server's worlds folder. The panel only asks when the server already has a
// world with the same name.

import { h, icon, api, toast, confirmDialog, fmtBytes, ago } from './dom.js';

const up = {
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
};
let appRef = null;
let lastDraw = 0;
let retryTimer = null;
export const recent = { names: [], until: 0, scrolled: false };

const busy = () => !['idle', 'done', 'error'].includes(up.phase);
const redraw = () => { if (appRef) appRef.render(); };

export function initUploads(app) {
  appRef = app;
  const A = app.actions;
  A['upload-pick'] = (d) => document.getElementById(d.input).click();
  A['upload-chosen'] = (d, el) => {
    const files = [...el.files].map((f) => ({ file: f, path: f.webkitRelativePath || f.name }));
    el.value = ''; // so choosing the same thing again still works
    start(files);
  };
  A['upload-decide'] = (d) => decide(d.id, d.choice);
  A['upload-cancel'] = cancel;
  A['upload-reset'] = reset;
  listenForDrops(app);
}

// ---------- dropping files anywhere in the window ----------

function listenForDrops(app) {
  const overlay = h('div', { id: 'drop-overlay', hidden: true, 'aria-hidden': 'true' },
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
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth++;
    show(true);
  });
  window.addEventListener('dragover', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = busy() ? 'none' : 'copy';
  });
  window.addEventListener('dragleave', (e) => {
    if (!hasFiles(e)) return;
    depth = Math.max(0, depth - 1);
    if (!depth) show(false);
  });
  window.addEventListener('drop', async (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth = 0;
    show(false);
    const pending = droppedFiles(e.dataTransfer); // must start before the event ends
    if (app.page.id !== 'worlds') app.go('worlds');
    start(await pending);
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

async function start(files) {
  if (!files || !files.length) return;
  if (busy()) {
    toast(`Please wait until ${up.label} has been added.`, 'warn');
    return;
  }
  Object.assign(up, {
    phase: 'uploading', label: describe(files), total: files.reduce((n, f) => n + f.file.size, 0), sent: 0,
    id: null, cands: [], choice: {}, job: 0, result: '', names: [], error: '',
  });
  redraw();
  try {
    up.id = (await api('/api/uploads', { method: 'POST', body: {} })).id;
    await sendFiles(up.id, files, (sent) => {
      up.sent = sent;
      if (Date.now() - lastDraw > 150) { lastDraw = Date.now(); redraw(); }
    });
    if (up.phase !== 'uploading') return; // cancelled
    up.phase = 'scanning';
    redraw();
    up.cands = (await api(`/api/uploads/${up.id}/scan`, { method: 'POST', body: {} })).candidates;
  } catch (e) {
    if (up.phase === 'idle') return; // cancelled
    return fail(e.message);
  }
  for (const c of up.cands) {
    if (c.problem || c.superseded || !c.name) up.choice[c.id] = 'skip';
    else if (!c.exists) up.choice[c.id] = 'import';
  }
  const usable = up.cands.filter((c) => !c.problem && !c.superseded && c.name);
  if (!usable.length) {
    const c = up.cands[0];
    return fail(c && c.problem ? c.problem : 'That world’s files don’t say what it’s called. Put them in a folder named after the world and add the folder.');
  }
  next();
}

function undecided() {
  return up.cands.filter((c) => !up.choice[c.id]);
}

function next() {
  if (undecided().length) {
    up.phase = 'deciding';
    redraw();
    return;
  }
  importNow();
}

async function decide(id, choice) {
  const c = up.cands.find((x) => x.id === id);
  if (!c || up.phase !== 'deciding') return;
  if (choice === 'replace' && c.inUse) {
    const st = appRef.status;
    const n = st ? st.players.length : 0;
    const ok = await confirmDialog({
      title: `Replace “${c.name}” now?`,
      message: `The server is playing this world. It saves and stops, swaps in the copy you added, and starts again (about a minute).${n ? ` The ${n} player${n === 1 ? '' : 's'} online will be disconnected.` : ''} The server’s current copy is kept as a backup.`,
      confirm: 'Replace and restart', icon: 'refresh',
    });
    if (!ok) return;
  }
  up.choice[id] = choice;
  next();
}

async function importNow() {
  clearTimeout(retryTimer);
  const choices = up.cands.map((c) => {
    const ch = up.choice[c.id];
    if (ch === 'keep') return { id: c.id, action: 'import', name: c.freeName };
    return { id: c.id, action: ch || 'skip' };
  });
  up.names = choices.filter((c) => c.action !== 'skip').map((c) => c.name || up.cands.find((x) => x.id === c.id).name);
  if (!up.names.length) {
    api('/api/uploads/' + up.id, { method: 'DELETE' }).catch(() => {});
    toast('Nothing was added.', 'info');
    return reset();
  }
  try {
    const res = await api(`/api/uploads/${up.id}/import`, { method: 'POST', body: { choices } });
    up.job = res.job;
    up.phase = 'importing';
    redraw();
    appRef.poll();
  } catch (e) {
    if (e.status === 409) { // another job is running: try again shortly
      up.phase = 'waiting';
      up.waitingFor = e.message;
      redraw();
      retryTimer = setTimeout(importNow, 2000);
      return;
    }
    fail(e.message);
  }
}

// uploadStatus follows the import job in the regular status updates.
export function uploadStatus(st) {
  if (up.phase !== 'importing' || !st.job) return;
  const job = st.job;
  if (job.id < up.job || (job.id === up.job && !job.finished)) return;
  if (job.id === up.job && job.error) return fail(job.error);
  // Finished (or a later job has already started, so this one is done).
  up.phase = 'done';
  up.result = job.id === up.job ? job.result : '';
  recent.names = up.names.slice();
  recent.until = Date.now() + 12000;
  recent.scrolled = false;
}

// ownsJob tells the app not to repeat this section's result as a message.
export function ownsJob(id) { return id === up.job && up.job > 0; }

function fail(msg) {
  up.phase = 'error';
  up.error = msg;
  if (up.id) api('/api/uploads/' + up.id, { method: 'DELETE' }).catch(() => {});
  up.id = null;
  redraw();
}

function cancel() {
  clearTimeout(retryTimer);
  if (up.xhr) up.xhr.abort();
  if (up.id) api('/api/uploads/' + up.id, { method: 'DELETE' }).catch(() => {});
  reset();
}

function reset() {
  clearTimeout(retryTimer);
  Object.assign(up, { phase: 'idle', id: null, xhr: null, cands: [], choice: {}, job: 0, error: '', result: '' });
  redraw();
}

function sendFiles(id, files, onProgress) {
  return new Promise((resolve, reject) => {
    const fd = new FormData();
    for (const f of files) fd.append('file', f.file, encodeURIComponent(f.path).replace(/%2F/gi, '/'));
    const xhr = new XMLHttpRequest();
    up.xhr = xhr;
    xhr.open('POST', `/api/uploads/${id}/files`);
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

export const whereHelp = () => [
  h('ol', { class: 'steps' },
    h('li', null, 'On the PC where the world was played, press ', h('kbd', null, 'Windows'), ' + ', h('kbd', null, 'R'), ', paste this and press Enter:',
      h('div', { class: 'mono small path-box' }, '%userprofile%\\AppData\\LocalLow\\IronGate\\Valheim\\worlds_local'),
      'In Valheim 1.0 each world is a folder there. Older worlds are two files, ', h('span', { class: 'mono' }, 'Name.fwl'), ' and ', h('span', { class: 'mono' }, 'Name.db'), '.'),
    h('li', null, 'Bring it to this computer: on a USB stick, or zip it and send it through a chat or cloud drive, then download it here.'),
    h('li', null, 'Drop the folder, the two files or the zip here.')),
  h('p', { class: 'small muted' }, 'Worlds kept in Steam Cloud must first be moved to local saves in Valheim (Manage saves). Played Valheim on this computer? Use ', h('b', null, 'Import from this PC'), '.'),
];

function pickers() {
  return [
    h('input', { type: 'file', id: 'upload-folder', webkitdirectory: '', multiple: true, hidden: true, 'data-action': 'upload-chosen', 'data-keep': '', 'data-key': 'pick-folder' }),
    h('input', { type: 'file', id: 'upload-files', multiple: true, accept: '.zip,.fwl,.db,.fwl2,.db2,.chunks,.chunk,.ok', hidden: true, 'data-action': 'upload-chosen', 'data-keep': '', 'data-key': 'pick-files' }),
  ];
}

function head(ic, title, sub) {
  return h('div', { class: 'add-world-head' },
    h('div', { class: 'add-world-icon' }, icon(ic)),
    h('div', { class: 'grow' }, h('h2', null, title), sub ? h('p', { class: 'text-2' }, sub) : null));
}

function progressBar(pct) {
  const known = pct != null && pct >= 0;
  return h('div', { class: `progress${known ? '' : ' indeterminate'}` }, h('i', { style: { width: (known ? pct : 35) + '%' } }));
}

function clashCard(c) {
  const yours = [c.modified ? `saved ${ago(c.modified)}` : '', fmtBytes(c.size)].filter(Boolean).join(' · ');
  const theirs = [c.serverModified ? `saved ${ago(c.serverModified)}` : '', c.serverSize ? fmtBytes(c.serverSize) : ''].filter(Boolean).join(' · ');
  const newer = c.modified && c.serverModified && new Date(c.modified) > new Date(c.serverModified);
  return h('div', { class: 'callout warn clash', 'data-key': 'clash:' + c.id }, icon('alert'),
    h('div', { class: 'body' },
      h('p', null, h('strong', null, `The server already has a world called “${c.name}”.`)),
      h('p', { class: 'small text-2' }, `The one you added: ${yours}. The server’s: ${theirs}.`,
        newer ? ' Yours is newer.' : ''),
      c.inUse ? h('p', { class: 'small text-2' }, 'The server is playing it right now. Replacing it restarts the server with your copy.') : null,
      h('div', { class: 'actions' },
        h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'upload-decide', 'data-id': c.id, 'data-choice': 'replace' }, icon('refresh'), 'Replace the server’s copy'),
        c.freeName ? h('button', { class: 'btn btn-sm', 'data-action': 'upload-decide', 'data-id': c.id, 'data-choice': 'keep' }, icon('plus'), `Keep both (add as “${c.freeName}”)`) : null,
        h('button', { class: 'btn btn-sm btn-ghost', 'data-action': 'upload-decide', 'data-id': c.id, 'data-choice': 'skip' }, 'Don’t add it')),
      h('p', { class: 'small muted', style: { marginTop: '8px' } }, 'Replacing keeps a backup of the server’s copy first, so it can be undone from the world’s Backups.')));
}

export function uploadSection(app) {
  const st = app.status;
  let body;
  switch (up.phase) {
    case 'uploading': {
      const pct = up.total ? Math.min(100, (up.sent / up.total) * 100) : -1;
      body = [
        head('upload', `Adding ${up.label}…`, `Copying ${fmtBytes(up.sent)} of ${fmtBytes(up.total)}`),
        progressBar(pct),
        h('div', { class: 'row add-world-actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'upload-cancel' }, 'Cancel')),
      ];
      break;
    }
    case 'scanning':
      body = [head('upload', `Adding ${up.label}…`, 'Looking for worlds…'), progressBar(-1)];
      break;
    case 'deciding': {
      const clashes = undecided();
      const adding = up.cands.filter((c) => up.choice[c.id] === 'import' || up.choice[c.id] === 'keep' || up.choice[c.id] === 'replace');
      const skipped = up.cands.filter((c) => c.superseded).length;
      body = [
        head('globe', clashes.length === 1 ? 'One question first' : `${clashes.length} questions first`,
          adding.length ? `Ready to add: ${adding.map((c) => `“${up.choice[c.id] === 'keep' ? c.freeName : c.name}”`).join(', ')}.` : null),
        clashes.map(clashCard),
        skipped ? h('p', { class: 'small muted' }, `${skipped} other cop${skipped === 1 ? 'y' : 'ies'} of the same world${skipped === 1 ? '' : 's'}, such as Valheim’s own backups, will be skipped.`) : null,
        h('div', { class: 'row add-world-actions' }, h('button', { class: 'btn btn-sm btn-ghost', 'data-action': 'upload-cancel' }, 'Cancel')),
      ];
      break;
    }
    case 'waiting':
      body = [
        head('clock', `Adding ${up.label}…`, up.waitingFor.replace(/^Please wait: /, 'Waiting: ')),
        progressBar(-1),
        h('div', { class: 'row add-world-actions' }, h('button', { class: 'btn btn-sm btn-ghost', 'data-action': 'upload-cancel' }, 'Cancel')),
      ];
      break;
    case 'importing': {
      const job = st && st.job && st.job.id === up.job ? st.job : null;
      body = [head('upload', job ? job.title + '…' : `Adding ${up.label}…`, job && job.message ? job.message : 'Putting it in the worlds folder…'), progressBar(-1)];
      break;
    }
    case 'done': {
      const one = up.names.length === 1 ? up.names[0] : null;
      const running = st && (st.state === 'online' || st.state === 'starting');
      const isRunning = one && st && st.runningWorld === one;
      body = [h('div', { class: 'callout good' }, icon('check'),
        h('div', { class: 'body' },
          h('p', null, h('strong', null, up.result || (one ? `“${one}” is on the server.` : 'The worlds are on the server.'))),
          h('div', { class: 'actions' },
            one && !isRunning ? h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'world-play', 'data-name': one }, icon('play'), running ? 'Switch to this world' : 'Play this world') : null,
            !one ? h('span', { class: 'small text-2' }, 'Choose one below to play it.') : null,
            h('button', { class: 'btn btn-sm', 'data-action': 'upload-reset' }, icon('plus'), 'Add another world'))))];
      break;
    }
    case 'error':
      body = [h('div', { class: 'callout bad' }, icon('alert'),
        h('div', { class: 'body' },
          h('p', null, h('strong', null, 'That world couldn’t be added. ')), h('p', null, up.error),
          h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'upload-reset' }, 'Try again')))),
      h('details', { class: 'where' }, h('summary', null, 'Where do I find a world?'), whereHelp())];
      break;
    default:
      body = [
        head('upload', 'Add a world', 'Drop a world here: its folder, its .fwl and .db files, or a .zip. It goes into the server’s worlds folder by itself.'),
        h('div', { class: 'row add-world-actions' },
          h('button', { class: 'btn btn-primary', 'data-action': 'upload-pick', 'data-input': 'upload-folder' }, icon('folder'), 'Choose a world folder'),
          h('button', { class: 'btn', 'data-action': 'upload-pick', 'data-input': 'upload-files' }, icon('upload'), 'Choose a zip or files')),
        h('details', { class: 'where' }, h('summary', null, 'Where do I find a world?'), whereHelp()),
      ];
  }
  // The file pickers come first so refreshes never replace them while one is open.
  return h('section', { class: `card add-world ${up.phase}`, 'aria-label': 'Add a world', 'data-key': 'add-world' }, pickers(), body);
}
