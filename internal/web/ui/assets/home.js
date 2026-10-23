// Home: server status and controls, how to join, who's online, live usage
// and the activity feed.

import { h, icon, api, fmtBytes, fmtRate, fmtPct, dur, ago, clock, initials } from './dom.js';
import { sparkline } from './charts.js';
import { jobLine, stateInfo } from './app.js';

let spark = { samples: [], at: 0, loading: false };

export const FEED_STYLE = {
  join: ['login', 'good'], leave: ['logout', ''], death: ['skull', 'bad'], online: ['power', 'good'],
  stopped: ['stop', ''], crashed: ['alert', 'bad'], saved: ['save', ''], wrong_password: ['key', 'warn'],
  bad_version: ['alert', 'warn'], load_failed: ['alert', 'bad'], bad_password_config: ['key', 'bad'],
  hibernate: ['moon', 'accent'], wake: ['sun', 'accent'], backup: ['archive', 'accent'], restore: ['archive', 'accent'],
  import: ['upload', 'accent'], delete: ['trash', ''], world: ['globe', 'accent'], update: ['refresh', 'accent'],
  warning: ['alert', 'warn'], starting: ['play', ''], info: ['info', ''],
};

function heroTitle(st) {
  const info = stateInfo(st);
  if (st.job && !st.job.finished && st.job.kind === 'install') return st.installed ? 'Updating…' : 'Installing…';
  switch (st.state) {
    case 'not_installed': return 'Not installed yet';
    case 'stopping': return 'Saving & stopping…';
  }
  if (info.label === 'Crashed') return 'Stopped unexpectedly';
  return info.label;
}

const ACTION_BUTTONS = {
  force_stop: ['force-stop', 'Force stop', 'danger'],
  install: ['install', 'Install the server', 'primary'],
  enable_hibernate: ['enable-hibernate', 'Turn on hibernation', 'primary'],
  settings: ['show-settings', 'Open settings', ''],
  log: ['show-log', 'Open the log', ''],
};

function hero(app) {
  const st = app.status;
  const info = stateInfo(st);
  const running = st.state === 'online' || st.state === 'starting';
  const job = st.job && !st.job.finished ? st.job : null;
  const failed = st.job && st.job.finished && st.job.error && !app.dismissed.has(st.job.id) &&
    Date.now() - new Date(st.job.finished) < 30 * 60e3 ? st.job : null;

  const facts = [];
  const world = st.runningWorld || st.world;
  facts.push(h('span', null, icon('globe'), `World: ${world}`));
  if (st.startedAt && running) facts.push(h('span', null, icon('clock'), `Up ${dur((Date.now() - new Date(st.startedAt)) / 1000)}`));
  if (st.lastSave && running) facts.push(h('span', null, icon('save'), `Saved ${ago(st.lastSave)}`));
  if (st.version) facts.push(h('span', null, icon('info'), `Valheim ${st.version}`));
  else if (st.buildDate && st.installed) facts.push(h('span', null, icon('refresh'), `Server updated ${ago(st.buildDate)}`));

  let actions;
  if (job) {
    actions = st.state === 'stopping' && Date.now() - new Date(job.started) > 30e3
      ? [h('button', { class: 'btn btn-danger', 'data-action': 'force-stop' }, icon('power'), 'Force stop')]
      : [];
  } else if (st.state === 'not_installed') {
    actions = [h('button', { class: 'btn btn-primary btn-lg', 'data-action': 'install' }, icon('download'), 'Install the server')];
  } else if (st.state === 'stopped') {
    actions = [h('button', { class: 'btn btn-primary btn-lg', 'data-action': 'start' }, icon('play'), 'Start server')];
  } else if (st.state === 'starting') {
    actions = [h('button', { class: 'btn btn-lg', 'data-action': 'stop' }, icon('stop'), 'Stop')];
  } else if (st.state === 'online') {
    actions = [
      h('button', { class: 'btn btn-lg', 'data-action': 'restart' }, icon('restart'), 'Restart'),
      h('button', { class: 'btn btn-lg', 'data-action': 'stop' }, icon('stop'), 'Stop server'),
    ];
  } else {
    actions = [h('button', { class: 'btn btn-danger', 'data-action': 'force-stop' }, icon('power'), 'Force stop')];
  }
  // Always here, next to the server buttons, so it's as easy to find as they are.
  actions.push(h('button', { class: 'btn btn-lg btn-guide', 'data-action': 'tutorial', title: 'A short guide to starting and stopping the server' },
    icon('help'), 'Tutorial'));

  const extra = [];
  if (job) extra.push(jobLine(job));
  if (failed) {
    const act = ACTION_BUTTONS[failed.action];
    extra.push(h('div', { class: 'callout bad jobline', role: 'alert' }, icon('alert'),
      h('div', { class: 'body' }, h('p', null, h('strong', null, failed.title + ' didn’t work. ')), h('p', null, failed.error),
        h('div', { class: 'actions' },
          act ? h('button', { class: `btn btn-sm ${act[2] ? 'btn-' + act[2] : ''}`, 'data-action': act[0] }, act[1]) : null,
          h('button', { class: 'btn btn-sm btn-ghost', 'data-action': 'dismiss-job', 'data-id': failed.id }, 'Dismiss')))));
  }
  if (st.needsRestart && st.state === 'online' && !job) {
    extra.push(h('div', { class: 'callout warn jobline' }, icon('refresh'),
      h('div', { class: 'body' }, h('p', null, 'Some settings changed while the server was running. Restart it to use them.'),
        h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'restart' }, 'Restart now')))));
  }
  if (st.state === 'stopped' && info.label === 'Crashed' && !job) {
    extra.push(h('div', { class: 'callout bad jobline' }, icon('alert'),
      h('div', { class: 'body' },
        h('p', null, `The server stopped unexpectedly at ${clock(st.lastExit.at)} (exit code ${st.lastExit.code}).`),
        h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm', 'data-action': 'show-log' }, icon('terminal'), 'See what happened')))));
  }
  if (st.state === 'not_installed' && !job) {
    extra.push(h('p', { class: 'text-2 small jobline' },
      'The free Valheim dedicated server (about 1 GB) is downloaded from Steam with Valve’s SteamCMD tool. You don’t need to own Valheim on this computer.'));
  }

  return h('section', { class: 'card hero', 'aria-label': 'Server status' },
    h('div', null,
      h('div', { class: 'hero-state' }, h('span', { class: `dot ${info.dot}` }), h('h2', null, heroTitle(st))),
      h('div', { class: 'facts' }, facts)),
    h('div', { class: 'hero-actions' }, actions),
    extra);
}

function joinCard(app) {
  const st = app.status;
  const j = st.join;
  const online = st.state === 'online';
  const pwId = 'join-pw';
  const pwRow = [
    h('dt', null, 'Password'),
    h('dd', null, h('span', { class: 'pw', id: pwId }, '•'.repeat(Math.min(12, j.password.length)))),
    h('dd', { class: 'row', style: { gap: '4px' } },
      h('button', { class: 'btn btn-ghost btn-icon btn-sm', 'data-action': 'reveal', 'data-target': pwId, 'data-secret': j.password, 'aria-label': 'Show password' }, icon('eye')),
      h('button', { class: 'btn btn-ghost btn-icon btn-sm', 'data-action': 'copy', 'data-text': j.password, 'data-label': 'Password copied', 'aria-label': 'Copy password' }, icon('copy'))),
  ];
  const addrRow = (label, addr) => [
    h('dt', null, label), h('dd', { class: 'mono' }, addr),
    h('dd', null, h('button', { class: 'btn btn-ghost btn-icon btn-sm', 'data-action': 'copy', 'data-text': addr, 'data-label': 'Address copied', 'aria-label': 'Copy address' }, icon('copy'))),
  ];
  const lan = (j.lanAddrs || []).filter((a) => !a.startsWith('100.'));
  const body = [];
  if (j.crossplay) {
    const code = j.code ? j.code.replace(/(\d{3})(\d{3})/, '$1 $2') : '';
    body.push(h('div', { class: 'row', style: { justifyContent: 'space-between', marginBottom: '14px' } },
      h('div', null, h('div', { class: 'muted small' }, 'Join code'),
        code ? h('div', { class: 'join-code' }, code)
          : h('div', { class: 'text-2', style: { padding: '8px 0' } }, online || st.state === 'starting' ? 'Waiting for the code…' : 'Shown here when the server is online.')),
      code ? h('button', { class: 'btn', 'data-action': 'copy', 'data-text': j.code, 'data-label': 'Join code copied' }, icon('copy'), 'Copy code') : null));
    body.push(h('dl', { class: 'kv' }, j.publicAddr ? addrRow('Or address', j.publicAddr) : null, pwRow));
    body.push(h('ol', { class: 'steps' },
      h('li', null, 'Open Valheim, pick your character and go to ', h('b', null, 'Join Game'), '.'),
      h('li', null, 'Choose to join by code and enter the code above (or add the server by its address).'),
      h('li', null, 'Enter the password. The code changes each time the server restarts.')));
  } else {
    body.push(h('dl', { class: 'kv' },
      j.publicAddr ? addrRow('Address', j.publicAddr) : null,
      lan.length ? addrRow('Same network', lan[0]) : null,
      pwRow));
    body.push(h('ol', { class: 'steps' },
      h('li', null, 'In Valheim go to ', h('b', null, 'Join Game'), ', add the server by its address and enter the password.'),
      h('li', null, 'Friends outside your home need UDP ports ', h('b', null, `${j.port}–${j.port + 1}`), ' forwarded on your router to this computer. Or turn on Crossplay in Settings to get a join code that works without port forwarding.')));
  }
  const mods = st.mods || [];
  if (mods.length) body.unshift(modsHelp(mods));
  return h('section', { class: 'card', 'aria-label': 'How to join' },
    h('div', { class: 'card-head' }, h('h2', null, 'How friends join'),
      h('span', { class: 'badge' }, j.crossplay ? 'Crossplay on' : 'Steam only')),
    body);
}

// modsHelp tells players how to get the mods. Valheim can't send mods to a
// player, so each player installs them once with a mod manager.
function modsHelp(mods) {
  const names = mods.join(' and ');
  return h('div', { class: 'callout info mods-help' }, icon('info'), h('div', { class: 'body small' },
    h('p', null, h('strong', null, `This world uses the ${names} mod${mods.length > 1 ? 's' : ''}. `),
      'Everyone needs it in their own game first (PC only; console players can’t join while it’s on):'),
    h('ol', { class: 'steps' },
      h('li', null, 'Install a free mod manager: ', h('b', null, 'r2modman'), ' or ', h('b', null, 'Gale'), ' (from thunderstore.io).'),
      h('li', null, 'Choose Valheim, search for ', h('b', null, mods.length > 1 ? 'each mod' : `“${names}”`), ' and install it (', h('b', null, 'Download'), ' in r2modman). BepInEx and Jötunn come with it.'),
      h('li', null, 'Start Valheim from the mod manager (', h('b', null, 'Start modded'), ' in r2modman), then join as usual.')),
    h('p', { class: 'muted', style: { marginTop: '6px' } }, 'If joining fails because the versions don’t match, update the mod in the mod manager. The server keeps it up to date.')));
}

function playersCard(app) {
  const st = app.status;
  const list = st.players.length
    ? h('div', { class: 'list' }, st.players.map((p) => h('div', { class: 'list-row', 'data-key': p.name },
      h('div', { class: 'avatar', 'aria-hidden': 'true' }, initials(p.name)),
      h('div', { class: 'grow' }, h('div', { class: 'name' }, p.name), h('div', { class: 'sub' }, `Playing for ${dur((Date.now() - new Date(p.since)) / 1000)}`)))))
    : h('div', { class: 'empty' }, icon('users'), st.state === 'online' ? 'Nobody’s playing right now.' : 'The server is offline.');
  return h('section', { class: 'card', 'aria-label': 'Players online' },
    h('div', { class: 'card-head' }, h('h2', null, 'Playing now'), h('span', { class: 'hint' }, `${st.players.length} of 10`)),
    list);
}

function tile(id, ic, label, value, unit, sub) {
  return h('div', { class: 'tile' },
    h('div', { class: 'label' }, icon(ic), label),
    h('div', { class: 'value' }, value, unit ? h('small', null, unit) : null),
    h('div', { class: 'sub' }, sub || ''),
    h('canvas', { 'data-spark': id, 'data-keep': '', 'aria-hidden': 'true' }));
}

function tiles(app) {
  const st = app.status;
  const L = st.live || {};
  const running = st.state === 'online' || st.state === 'starting';
  const gpuNames = (st.machine && st.machine.gpus) || [];
  return h('section', { class: 'tiles', 'aria-label': 'Live usage' },
    tile('cpu', 'cpu', 'Server CPU', running ? fmtPct(L.cpu) : '–', '', running && L.cores ? `${L.cores.toFixed(1)} cores busy` : 'Valheim server'),
    tile('mem', 'memory', 'Server memory', running ? fmtBytes(L.mem) : '–', '', 'Valheim server'),
    tile('cpuSys', 'cpu', 'Computer CPU', fmtPct(L.cpuSys), '', st.machine ? `${st.machine.threads} threads` : ''),
    tile('memSys', 'memory', 'Computer memory', fmtBytes(L.memSys), '', L.memTotal ? `of ${fmtBytes(L.memTotal)}` : ''),
    tile('gpu', 'gpu', 'Graphics (GPU)', L.gpuOk ? fmtPct(L.gpu) : '–', '', L.gpuOk ? (gpuNames.length ? gpuNames.join(' + ') : 'busiest card') : 'not available'),
    tile('net', 'network', 'Network', L.netOk ? fmtRate(L.netUp) : '–', '', L.netOk ? `up · ${fmtRate(L.netDown)} down` : 'not available'));
}

function feed(app) {
  const st = app.status;
  const items = st.activity || [];
  return h('section', { class: 'card', 'aria-label': 'Activity' },
    h('div', { class: 'card-head' }, h('h2', null, 'Activity')),
    items.length
      ? h('div', { class: 'feed' }, items.map((a) => {
        const [ic, tone] = FEED_STYLE[a.kind] || FEED_STYLE.info;
        return h('div', { class: 'feed-item', 'data-key': String(a.seq) },
          h('div', { class: `feed-icon ${tone}` }, icon(ic)),
          h('div', { class: 'feed-text' }, a.text),
          h('div', { class: 'feed-time', title: new Date(a.time).toLocaleString() }, ago(a.time)));
      }))
      : h('div', { class: 'empty' }, 'Nothing has happened yet.'));
}

function checklist(app) {
  const st = app.status;
  const c = st.checklist;
  if (localStorage.getItem('ves-setup-hidden') === '1') return null;
  const items = [
    [c.installed, 'Install the Valheim server', 'Free, downloaded from Steam.', !c.installed && h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'install' }, 'Install')],
    [c.firewall, 'Allow it through Windows Firewall', 'So friends can connect. Windows asks for permission.', !c.firewall && h('button', { class: 'btn btn-sm', 'data-action': 'fix-firewall' }, 'Allow')],
    st.restMode === 'sleep'
      ? [st.sleep.available, 'Sleep works on this computer', st.sleep.available ? 'The Sleep button works.' : st.sleep.detail, !st.sleep.available && h('button', { class: 'btn btn-sm', 'data-action': 'go', 'data-page': 'settings' }, 'Settings')]
      : [c.hibernate, 'Hibernation turned on', c.hibernate ? 'The Hibernate button works.' : 'Needed for the Hibernate button.', !c.hibernate && h('button', { class: 'btn btn-sm', 'data-action': 'enable-hibernate' }, 'Turn on')],
    [c.wakeOnLan, 'Wake-on-LAN turned on', c.wired ? 'So the computer can be woken over the network, for example from an IP KVM.' : 'Needs a network cable: Wake-on-LAN doesn’t work over Wi-Fi.',
      !c.wakeOnLan && c.wired && h('button', { class: 'btn btn-sm', 'data-action': 'enable-wol' }, 'Turn on')],
    [c.launchOnLogin, 'Start this app with Windows', 'So it’s ready after a restart.', !c.launchOnLogin && h('button', { class: 'btn btn-sm', 'data-action': 'go', 'data-page': 'settings' }, 'Set up')],
  ];
  const left = items.filter((i) => !i[0]).length;
  return h('section', { class: 'card', 'aria-label': 'Setup' },
    h('div', { class: 'card-head' }, h('h2', null, 'Setup on this computer'),
      h('span', { class: 'hint' }, left ? `${left} left` : 'All done'),
      h('button', { class: 'btn btn-ghost btn-sm', 'data-action': 'hide-setup' }, 'Hide')),
    items.map(([done, title, help, btn]) => h('div', { class: `check-item${done ? ' done' : ''}` },
      h('div', { class: 'check-mark' }, icon(done ? 'check' : 'more')),
      h('div', { class: 'grow' }, h('div', { class: 'title' }, title), h('div', { class: 'help' }, help)),
      btn || null)));
}

function idleBanner(app) {
  const st = app.status;
  if (!st.idle || st.players.length) return null;
  const mins = Math.max(0, Math.round((new Date(st.idle.hibernateAt) - Date.now()) / 60000));
  return h('div', { class: 'callout warn' }, icon('moon'),
    h('div', { class: 'body' }, h('p', null, h('strong', null, 'Nobody is playing. '),
      st.restMode === 'sleep'
        ? (mins > 0 ? `This computer goes to sleep automatically in ${mins} min.` : 'This computer is about to go to sleep.')
        : (mins > 0 ? `This computer hibernates automatically in ${mins} min.` : 'This computer is about to hibernate.'))),
    h('button', { class: 'btn btn-sm', 'data-action': 'keep-awake' }, icon('sun'), 'Keep it awake'));
}

function warnings(app) {
  return (app.status.warnings || []).map((w) => h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body' }, w)));
}

export default {
  id: 'home',
  title: 'Home',
  heading: 'Server',
  icon: 'home',
  render(app) {
    if (!app.status) return h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Connecting…'));
    return h('div', { class: 'stack' },
      hero(app),
      warnings(app),
      idleBanner(app),
      h('div', { class: 'grid grid-2' }, joinCard(app), playersCard(app)),
      tiles(app),
      h('div', { class: 'grid grid-2' }, feed(app), checklist(app) || machineCard(app)));
  },
  mount(app) {
    app.actions['hide-setup'] = () => { localStorage.setItem('ves-setup-hidden', '1'); app.render(); };
  },
  afterRender(app) {
    if (Date.now() - spark.at > 10000 && !spark.loading) {
      spark.loading = true;
      api('/api/stats?range=15m').then((d) => { spark = { samples: d.samples, at: Date.now(), loading: false }; drawSparks(); })
        .catch(() => { spark.loading = false; });
    }
    drawSparks();
  },
};

function machineCard(app) {
  const m = app.status.machine || {};
  return h('section', { class: 'card', 'aria-label': 'This computer' },
    h('div', { class: 'card-head' }, h('h2', null, 'Server computer')),
    h('dl', { class: 'kv', style: { gridTemplateColumns: 'auto 1fr' } },
      h('dt', null, 'Name'), h('dd', null, m.hostname || '–'),
      h('dt', null, 'Processor'), h('dd', null, m.cpu || '–'),
      h('dt', null, 'Memory'), h('dd', null, m.memTotal ? fmtBytes(m.memTotal) : '–'),
      h('dt', null, 'Graphics'), h('dd', null, (m.gpus || []).join(', ') || '–'),
      h('dt', null, 'System'), h('dd', null, m.os || '–')));
}

function drawSparks() {
  const s = spark.samples;
  if (!s.length) return;
  const run = (v, x) => (x.running ? v : null);
  const series = {
    cpu: [s.map((x) => run(x.cpu, x))],
    mem: [s.map((x) => run(x.mem, x))],
    cpuSys: [s.map((x) => x.cpuSys), 100],
    memSys: [s.map((x) => x.memSys)],
    gpu: [s.map((x) => x.gpu), 100],
    net: [s.map((x) => x.netUp)],
  };
  for (const c of document.querySelectorAll('canvas[data-spark]')) {
    const [vals, max] = series[c.dataset.spark] || [[]];
    sparkline(c, vals, '#3987e5', max);
  }
}
