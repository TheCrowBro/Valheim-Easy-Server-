// Sleep and waking (APP group): the computer sleeps only when nobody plays
// any game for the chosen time and nothing is downloading or backing up
// (decisions.md); how it's woken again (Wake-on-LAN, from the JetKVM or
// Discord); and whether the games that were running start again.

import { h, icon, api, toast, toggle } from './dom.js';
import { callout, isMac, restWord } from './parts.js';

let appRef = null;
let view = null; // GET /api/settings
let minutes = '';
let saving = '';

const here = () => appRef && appRef.route.view === 'app' && appRef.route.page === 'sleep';

async function load() {
  try {
    view = await api('/api/settings');
    minutes = String(view.settings.idleMinutes || 60);
  } catch (e) {
    toast(e.message, 'bad');
  }
  if (here()) appRef.render();
}

// save sends one app-wide setting (POST /api/app/settings takes only what
// changed).
async function save(change, okMsg) {
  saving = Object.keys(change)[0];
  appRef.render();
  try {
    await api('/api/app/settings', { method: 'POST', body: change });
    toast(okMsg || 'Saved.', 'good');
  } catch (e) {
    toast(e.message, 'bad', 9000);
  } finally {
    saving = '';
    await load();
    appRef.poll();
  }
}

function idleCard(st) {
  const s = view.settings;
  const sleep = restWord(st) === 'Sleep';
  const n = Number(minutes);
  const bad = !(n >= 10 && n <= 1440);
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, icon('moon'), h('h2', null, sleep ? 'Sleeping when nobody plays' : 'Resting when nobody plays')),
    h('div', { class: 'toggle-row' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, sleep ? 'Sleep when nobody is playing any game' : 'Hibernate when nobody is playing any game'),
        h('div', { class: 'help' }, 'Saves power. It waits while anyone plays any game, or while something downloads or backs up. Once woken it stays awake for at least 15 minutes.'),
        s.idleHibernate ? h('div', { class: 'row', style: { marginTop: '8px' } }, 'After',
          h('input', { class: `input${bad ? ' invalid' : ''}`, type: 'number', min: 10, max: 1440, value: minutes, style: { width: '96px' }, 'aria-label': 'Minutes',
            oninput: (e) => { minutes = e.target.value; appRef.render(); } }),
          'minutes without players',
          String(n) !== String(s.idleMinutes) ? h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'sleep-minutes', disabled: bad || !!saving }, 'Save') : null,
          bad ? h('div', { class: 'error', style: { flexBasis: '100%' } }, 'Between 10 and 1440 minutes.') : null) : null),
      toggle({ checked: !!s.idleHibernate, 'data-action': 'sleep-idle', 'aria-label': 'Sleep when nobody is playing', disabled: !!saving })),
    st.idle && !st.playing ? h('div', { class: 'row', style: { marginTop: '8px' } },
      h('span', { class: 'small text-2' }, 'Nobody is playing, so the computer rests soon.'),
      h('button', { class: 'btn btn-sm', 'data-action': 'keep-awake' }, icon('sun'), 'Keep it awake')) : null,
    st.sleep && !st.sleep.available ? callout('warn', 'alert', h('p', null, st.sleep.detail || 'Sleep doesn’t work on this computer.')) : null,
    !sleep ? callout('info', 'info', h('p', null, 'This computer hibernates instead of sleeping (a choice made in an older version of the app). Most computers can’t be woken over the network from hibernation.'),
      h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'sleep-mode', disabled: !!saving }, 'Sleep instead'))) : null);
}

function wakeCard(st) {
  const s = view.settings;
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, icon('sun'), h('h2', null, 'After it wakes up')),
    h('div', { class: 'toggle-row' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, 'Start the games again that were running'),
        h('div', { class: 'help' }, 'When the computer is woken with Wake-on-LAN (for example the JetKVM’s button), the games that were running when it went to sleep start again. Waking it from Discord for one game starts only that game.')),
      toggle({ checked: !!s.startAfterWake, 'data-action': 'sleep-after-wake', 'aria-label': 'Start the games again after waking', disabled: !!saving })),
    h('p', { class: 'small muted', style: { marginTop: '10px' } }, 'Friends can wake it themselves with a game’s command in Discord: see ', h('a', { href: '#/app/discord' }, 'Discord'), '.'));
}

function wolCard(st) {
  const mac = isMac(st);
  const wired = (st.network || []).filter((a) => a.wired);
  const main = wired.find((a) => a.connected);
  const others = wired.filter((a) => a !== main);
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, icon('network'), h('h2', null, 'Wake-on-LAN')),
    main ? [
      h('p', { class: 'text-2' }, 'To wake this computer from your IP KVM (or any Wake-on-LAN app), use this address:'),
      h('div', { class: 'row', style: { gap: '8px', margin: '8px 0' } },
        h('span', { class: 'mac mono' }, main.mac),
        h('button', { class: 'btn btn-sm', 'data-action': 'copy', 'data-text': main.mac, 'data-label': 'Address copied' }, icon('copy'), 'Copy')),
      h('p', { class: 'small muted' }, [main.name, main.description !== main.name ? main.description : '', (main.ipv4 || [])[0] ? 'IP ' + main.ipv4[0] : ''].filter(Boolean).join(' · ')),
      main.wakeArmed
        ? callout('good', 'check', h('p', null, mac ? '“Wake for network access” is on: a Wake-on-LAN signal wakes this Mac.' : 'Windows lets this network adapter wake the computer.'),
          h('p', { class: 'small text-2' }, 'Test it once: use the Sleep button, wait a minute, then send Wake-on-LAN from the IP KVM.'))
        : callout('warn', 'alert', h('p', null, mac ? '“Wake for network access” is off, so this Mac can’t be woken over the network yet.' : 'Windows doesn’t let this network adapter wake the computer yet.'),
          h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'enable-wol' }, icon('power'), mac ? 'Turn on Wake for network access' : 'Turn on Wake-on-LAN')),
          h('p', { class: 'small text-2', style: { marginTop: '8px' } }, mac ? 'macOS asks for an administrator’s password. Nobody playing is disconnected.' : 'Windows asks for permission. Nobody playing is disconnected. If it still doesn’t wake afterwards, restart the computer once.')),
      others.length ? h('p', { class: 'small muted', style: { marginTop: '10px' } }, 'Other network ports: ', others.map((a) => `${a.name} (${a.mac}${a.connected ? '' : ', no cable'})`).join(', ')) : null,
    ] : callout('warn', 'alert', h('p', null, h('strong', null, 'Wake-on-LAN needs a network cable. ')),
      h('p', null, 'No wired network connection was found. Connect the computer to the router with an Ethernet cable: Wake-on-LAN doesn’t work over Wi-Fi.')));
}

export default {
  title: 'Sleep and waking',
  render(app) {
    appRef = app;
    const st = app.status;
    if (!view) return h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading…'));
    return h('div', { class: 'stack' }, idleCard(st), wakeCard(st), wolCard(st));
  },
  mount(app) {
    appRef = app;
    load();
    const A = app.actions;
    A['sleep-idle'] = (d, el) => save({ idleHibernate: el.checked }, el.checked ? 'The computer sleeps by itself when nobody plays.' : 'The computer stays awake.');
    A['sleep-minutes'] = () => save({ idleMinutes: Number(minutes) });
    A['sleep-after-wake'] = (d, el) => save({ startAfterWake: el.checked });
    // The rest mode is one of the old settings (settings.json restMode),
    // which only the old settings route saves, whole. It's read just
    // before, so nothing else changes.
    A['sleep-mode'] = async () => {
      saving = 'restMode';
      app.render();
      try {
        const cur = await api('/api/settings');
        await api('/api/settings', { method: 'POST', body: { ...cur.settings, restMode: 'sleep' } });
        toast('The computer now sleeps instead of hibernating.', 'good');
      } catch (e) {
        toast(e.message, 'bad', 9000);
      } finally {
        saving = '';
        await load();
        app.poll();
      }
    };
  },
};
