// Add a game (spec 6.1): every game this version of the app knows, saying
// whether it runs on this computer (greyed out, with why, when it can't),
// how big it is, the memory it needs and what friends need. Choosing one
// asks its server name and password, its licence where it has one, and its
// first world: a new one, or one from the owner's own copy of the game.
// "Star it" is unticked unless the owner ticks it (decisions.md).

import { h, icon, api, toast } from './dom.js';
import { loadCatalog, catalog, catalogGame } from './catalog.js';
import { tile, fmtMB, enc } from './state.js';
import { schemaForm } from './forms.js';
import { callout, loading, isMac } from './parts.js';

const forms = new Map(); // game -> what's typed in its Add form
let appRef = null;
let error = '';

function formFor(id) {
  let f = forms.get(id);
  if (!f) {
    forms.set(id, f = { values: {}, world: { values: {} }, errors: {}, mode: 'new', worldName: '', candidate: '', eula: false, star: false,
      local: null, localLoading: false, adding: false });
  }
  return f;
}

function runsHere(st) {
  if (isMac(st)) return 'Runs on this Mac';
  if (st && st.platform === 'windows') return 'Runs on this PC';
  return 'Runs on this computer';
}

function facts(app, c) {
  const can = c.canRun !== 'no';
  return h('div', { class: 'facts' },
    can ? h('span', { class: 'ok' }, icon('check'), runsHere(app.status)) : h('span', { class: 'warnt' }, icon('alert'), c.needsText || 'Can’t run on this computer'),
    c.downloadMB ? h('span', null, `About ${fmtMB(c.downloadMB)} to download`) : null,
    c.diskMB ? h('span', null, `${fmtMB(c.diskMB)} of disk`) : null,
    c.memoryMB ? h('span', null, `${fmtMB(c.memoryMB)} of memory`) : null,
    c.maxPlayers ? h('span', null, `Up to ${c.maxPlayers} players`) : null);
}

function gameItem(app, c) {
  const can = c.canRun !== 'no';
  const inst = c.added ? (app.status.games || []).find((g) => g.game === c.id) : null;
  let btn;
  if (c.added) btn = inst ? h('a', { class: 'btn btn-sm', href: `#/game/${enc(inst.id)}/home` }, 'Added', icon('chevronRight')) : h('span', { class: 'btn btn-sm', 'aria-disabled': 'true' }, 'Added');
  else if (can) btn = h('a', { class: 'btn btn-sm btn-primary', href: `#/add/${enc(c.id)}` }, icon('plus'), 'Add');
  else btn = h('button', { class: 'btn btn-sm', disabled: true, title: c.needsText || null }, 'Add');
  return h('article', { class: `gi${can ? '' : ' off'}`, 'data-key': 'gi:' + c.id },
    tile(c, 'lg'),
    h('div', { class: 'grow' },
      h('div', { class: 'gi-name' }, h('b', null, c.name),
        c.stage === 'preview' ? h('span', { class: 'badge accent', title: 'New in this version: it works, and is still being tried out with real servers' }, 'Preview') : null),
      facts(app, c),
      c.friendsNeed ? h('div', { class: 'small text-2' }, 'Friends need ', c.friendsNeed.replace(/\.$/, ''), '.') : null),
    btn);
}

function listPage(app) {
  const list = catalog();
  if (!list) return error ? callout('bad', 'alert', h('p', null, error)) : loading('Loading the games…');
  return h('div', { class: 'stack' },
    h('p', { class: 'text-2' }, 'Pick a game and the app downloads its server, with anything it needs to run (Java, .NET…). Each game then has its own Start and Stop, and runs beside the others.'),
    h('div', { class: 'gl' }, list.map((c) => gameItem(app, c))),
    list.some((c) => c.canRun === 'no')
      ? h('p', { class: 'small muted' }, 'Greyed-out games can’t run on this computer. Their servers only exist for other systems; add them on a computer that can run them.') : null);
}

// worldSection asks for the first world: a new one, or one from the
// owner's own game.
function worldSection(c, f) {
  const newFields = (c.schema || []).filter((x) => x.scope === 'newWorld');
  const radio = (mode, title, help) => h('label', { class: `choice${f.mode === mode ? ' on' : ''}` },
    h('input', { type: 'radio', name: 'add-world-mode', checked: f.mode === mode, 'data-action': 'add-mode', 'data-game': c.id, 'data-mode': mode }),
    h('div', null, h('div', { class: 'title' }, title), h('div', { class: 'help' }, help)));
  const out = [h('h3', { class: 'subhead' }, 'The first world')];
  if (c.import) {
    out.push(h('div', { class: 'choices' },
      radio('new', 'A new world', `${c.short} makes it the first time it starts.`),
      radio('import', 'From your own game', `A world you played in ${c.short} on this computer. It’s copied; yours stays as it is.`)));
  }
  if (f.mode === 'new' || !c.import) {
    out.push(h('div', { class: 'form', style: { marginTop: '12px' } },
      h('div', { class: 'field' }, h('label', { for: 'add-world-name' }, 'World name'),
        h('input', { class: `input${f.errors['world.name'] ? ' invalid' : ''}`, id: 'add-world-name', value: f.worldName, maxlength: 64, placeholder: 'For example Riverbend',
          oninput: (e) => { f.worldName = e.target.value; delete f.errors['world.name']; } }),
        f.errors['world.name'] ? h('div', { class: 'error' }, f.errors['world.name']) : h('div', { class: 'help' }, 'Leave it empty for the game’s usual name.')),
      newFields.length ? schemaForm(newFields, f.world, { id: 'add-nw', prefix: 'newWorld.' }) : null));
  } else if (f.localLoading) {
    out.push(h('p', { class: 'muted', style: { marginTop: '12px' } }, `Looking for ${c.short} worlds on this computer…`));
  } else if (f.local && !f.local.length) {
    out.push(callout('info', 'info', h('p', { class: 'small' }, `No ${c.short} worlds were found on this computer. Start with a new world: you can add one later on its Worlds tab, from a zip or a folder.`)));
  } else if (f.local) {
    out.push(h('div', { class: 'cands', role: 'radiogroup', 'aria-label': 'Worlds found' }, f.local.map((w) => h('label', { class: `choice${f.candidate === w.id ? ' on' : ''}${w.problem ? ' disabled' : ''}`, 'data-key': 'cand:' + w.id },
      h('input', { type: 'radio', name: 'add-cand', checked: f.candidate === w.id, disabled: !!w.problem, 'data-action': 'add-cand', 'data-game': c.id, 'data-cand': w.id }),
      h('div', null, h('div', { class: 'title' }, w.name || 'Unnamed world'),
        h('div', { class: 'help' }, w.problem || [w.source, w.note].filter(Boolean).join(' · ')))))));
  }
  if (f.errors.world) out.push(h('div', { class: 'error' }, f.errors.world));
  return out;
}

function formPage(app, id) {
  const list = catalog();
  if (!list) return error ? callout('bad', 'alert', h('p', null, error)) : loading();
  const c = catalogGame(id);
  if (!c) {
    return h('div', { class: 'card empty-state' }, h('h2', null, 'This game isn’t in the list'),
      h('p', { class: 'text-2' }, 'This version of the app doesn’t know it.'), h('a', { class: 'btn', href: '#/add' }, icon('arrowLeft'), 'All games'));
  }
  const f = formFor(c.id);
  const head = h('section', { class: 'card add-head', style: { '--g': c.color } },
    h('div', { class: 'row', style: { flexWrap: 'nowrap', alignItems: 'flex-start' } }, tile(c, 'xl'),
      h('div', { class: 'grow' }, h('h2', null, c.name), facts(app, c),
        c.friendsNeed ? h('p', { class: 'small text-2', style: { marginTop: '6px' } }, 'Friends need ', c.friendsNeed.replace(/\.$/, ''), '.') : null)));
  const back = h('a', { class: 'btn btn-ghost btn-sm', href: '#/add' }, icon('arrowLeft'), 'All games');
  if (c.canRun === 'no') return h('div', { class: 'stack' }, back, head, callout('warn', 'alert', h('p', null, h('strong', null, (c.needsText || 'It can’t run on this computer') + '. '), `${c.short}’s server only exists for other systems.`)));
  if (c.added) {
    const inst = (app.status.games || []).find((g) => g.game === c.id);
    return h('div', { class: 'stack' }, back, head, callout('good', 'check', h('p', null, `${c.short} is already added.`),
      inst ? h('div', { class: 'actions' }, h('a', { class: 'btn btn-sm', href: `#/game/${enc(inst.id)}/home` }, 'Open it')) : null));
  }
  const serverFields = (c.schema || []).filter((x) => x.scope === 'server');
  const pw = c.password || {};
  const other = Object.entries(f.errors).filter(([k]) => !k.startsWith('server.') && !k.startsWith('newWorld.') && !k.startsWith('world') && k !== 'eula');
  return h('div', { class: 'stack' },
    back,
    head,
    h('section', { class: 'card' },
      h('div', { class: 'card-head' }, h('h2', null, 'Its server')),
      other.length ? callout('bad', 'alert', other.map(([, m]) => h('p', null, m))) : null,
      h('div', { class: 'form' },
        schemaForm(serverFields, f, { id: 'add-' + c.id, prefix: 'server.' }),
        pw.use === 'none' && pw.noneText ? callout('info', 'shield', h('p', { class: 'small' }, pw.noneText)) : null),
      worldSection(c, f),
      c.eula ? h('label', { class: `key-option eula${f.errors.eula ? ' invalid' : ''}`, style: { marginTop: '18px' } },
        h('input', { type: 'checkbox', checked: f.eula, 'data-action': 'add-eula', 'data-game': c.id }),
        h('span', null, `I accept ${c.short}’s licence for running its server (`, h('a', { href: c.eula, target: '_blank', rel: 'noopener noreferrer' }, 'read it'), ').',
          h('small', null, 'The server can’t run without it.'), f.errors.eula ? h('small', { class: 'error' }, f.errors.eula) : null)) : null,
      h('label', { class: 'key-option', style: { marginTop: '14px' } },
        h('input', { type: 'checkbox', checked: f.star, 'data-action': 'add-star', 'data-game': c.id }),
        h('span', null, `Star it: show it under Active and add /${c.command || c.id} in Discord`,
          h('small', null, 'Stars are for the games you’re playing now. You can star it any time.'))),
      h('div', { class: 'row', style: { marginTop: '20px' } },
        h('button', { class: 'btn btn-primary btn-lg', 'data-action': 'add-submit', 'data-game': c.id, disabled: f.adding || (c.eula && !f.eula) },
          icon('download'), f.adding ? 'Adding…' : `Add ${c.short} and install it`),
        h('span', { class: 'small muted' }, `About ${fmtMB(c.downloadMB)} is downloaded.`))));
}

async function loadLocal(id) {
  const f = formFor(id);
  if (f.local || f.localLoading) return;
  f.localLoading = true;
  appRef.render();
  try {
    f.local = (await api(`/api/catalog/${enc(id)}/local`)).candidates || [];
    const first = f.local.find((w) => !w.problem);
    if (first && !f.candidate) f.candidate = first.id;
  } catch (e) {
    f.local = [];
    toast(e.message, 'bad', 8000);
  } finally {
    f.localLoading = false;
    appRef.render();
  }
}

async function submit(app, id) {
  const c = catalogGame(id);
  const f = formFor(id);
  if (!c || f.adding) return;
  const values = {};
  for (const x of (c.schema || []).filter((s) => s.scope === 'server' && s.key !== 'serverName' && s.key !== 'password')) {
    if (f.values[x.key] != null) values[x.key] = String(f.values[x.key]);
  }
  const newFields = (c.schema || []).filter((x) => x.scope === 'newWorld');
  const worldValues = {};
  for (const x of newFields) worldValues[x.key] = f.world.values[x.key] != null ? String(f.world.values[x.key]) : x.default || '';
  const importing = c.import && f.mode === 'import';
  if (importing && !f.candidate) {
    f.errors.world = 'Choose a world, or start with a new one.';
    app.render();
    return;
  }
  const cand = importing ? (f.local || []).find((w) => w.id === f.candidate) : null;
  const sv = (k) => (f.values[k] != null ? String(f.values[k]) : ((c.schema || []).find((x) => x.key === k) || {}).default || '');
  const body = {
    game: c.id, serverName: sv('serverName').trim(), password: sv('password'), values, eula: !!f.eula, star: !!f.star,
    world: importing ? { mode: 'import', name: cand ? cand.name : '', candidate: f.candidate } : { mode: 'new', name: f.worldName.trim(), values: worldValues },
  };
  f.adding = true;
  f.errors = {};
  app.render();
  try {
    const res = await api('/api/games', { method: 'POST', body });
    forms.delete(id);
    toast(`${c.short} was added. Its server is being installed.`, 'good', 8000);
    loadCatalog(true).catch(() => {});
    await app.poll();
    app.go(`game/${enc(res.id || c.id)}/home`);
  } catch (e) {
    f.errors = (e.data && e.data.fields) || {};
    if (!Object.keys(f.errors).length) f.errors[''] = e.message;
    toast(e.message, 'bad', 10000);
  } finally {
    f.adding = false;
    app.render();
  }
}

export default {
  title: 'Add a game',
  heading(app) {
    const c = app.route.game ? catalogGame(app.route.game) : null;
    return c ? ['Add ', c.short] : 'Add a game';
  },
  render(app) {
    appRef = app;
    return app.route.game ? formPage(app, app.route.game) : listPage(app);
  },
  mount(app) {
    appRef = app;
    error = '';
    loadCatalog(true).then(() => app.render()).catch((e) => { error = e.message; app.render(); });
    const A = app.actions;
    A['add-mode'] = (d) => {
      const f = formFor(d.game);
      f.mode = d.mode;
      delete f.errors.world;
      app.render();
      if (d.mode === 'import') loadLocal(d.game);
    };
    A['add-cand'] = (d) => { const f = formFor(d.game); f.candidate = d.cand; delete f.errors.world; app.render(); };
    A['add-eula'] = (d, el) => { const f = formFor(d.game); f.eula = el.checked; delete f.errors.eula; app.render(); };
    A['add-star'] = (d, el) => { formFor(d.game).star = el.checked; app.render(); };
    A['add-submit'] = (d) => submit(app, d.game);
  },
};
