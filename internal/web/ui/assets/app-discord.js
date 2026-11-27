// Discord (APP group, spec 6.1 and 6.3-6.4): the main channel every game
// without a channel of its own posts in, which channel each game posts in,
// and starting games from Discord through the waker on the JetKVM (one
// command per starred game, working only in that game's channel).

import { h, icon, api, toast, confirmDialog, ago } from './dom.js';
import { tile, enc } from './state.js';
import { callout, isMac } from './parts.js';

let appRef = null;
let settings = null;       // GET /api/settings (the app-wide settings in settings.json)
let main = { webhook: '', loaded: false, error: '' };
let testing = false;
let saving = false;
const channels = new Map(); // instance -> its Discord settings, from its Settings tab

// The waker on the JetKVM. Its fields aren't settings: they're used when
// installing it.
let wk = null;
let wkForm = { address: '', token: '' };
let wkErrors = {};
let wkBusy = '';
let wkChecking = false;
let wkTimer = null;

const here = () => appRef && appRef.route.view === 'app' && appRef.route.page === 'discord';
const redraw = () => { if (here()) appRef.render(); };

async function loadMain() {
  try {
    settings = await api('/api/settings');
    if (!main.loaded) main.webhook = (settings.settings && settings.settings.discordWebhook) || '';
    main.loaded = true;
  } catch (e) {
    main.error = e.message;
  }
  redraw();
}

async function loadChannels() {
  const games = (appRef.status && appRef.status.games) || [];
  await Promise.all(games.map(async (g) => {
    try {
      const s = await api(`/api/games/${enc(g.id)}/settings`);
      channels.set(g.id, s.discord || {});
    } catch {
      channels.delete(g.id);
    }
  }));
  redraw();
}

async function loadWaker(live) {
  clearTimeout(wkTimer);
  wkChecking = live;
  redraw();
  try {
    wk = await api('/api/jetkvm' + (live ? '?live=1' : ''));
    if (!wkForm.address && wk.address) wkForm.address = wk.address;
  } catch (e) {
    toast(e.message, 'bad');
  } finally {
    wkChecking = false;
  }
  redraw();
  clearTimeout(wkTimer);
  // While the app is updating the waker nothing else says when it's done.
  if (wk && wk.busy && here()) wkTimer = setTimeout(() => { if (!wkChecking) loadWaker(false); }, 3000);
}

function mainCard() {
  const link = main.webhook.trim();
  const saved = ((settings && settings.settings && settings.settings.discordWebhook) || '').trim();
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, icon('message'), h('h2', null, 'The main channel'), saved ? h('span', { class: 'badge good' }, 'Set') : h('span', { class: 'badge' }, 'Not set')),
    h('p', { class: 'text-2', style: { marginBottom: '12px' } }, 'Games without a channel of their own post here when they’re online, with how to join. Leave it empty for no posts.'),
    h('div', { class: 'form discord-form' },
      h('div', { class: 'field' }, h('label', { for: 'main-hook' }, 'The channel’s webhook link'),
        h('div', { class: 'input-group' },
          h('input', { class: `input${main.error ? ' invalid' : ''}`, id: 'main-hook', value: main.webhook, placeholder: 'https://discord.com/api/webhooks/…', spellcheck: 'false', autocomplete: 'off',
            oninput: (e) => { main.webhook = e.target.value; main.error = ''; appRef.render(); } }),
          h('button', { class: 'btn', type: 'button', 'data-action': 'main-test', disabled: testing || !link }, icon('send'), testing ? 'Sending…' : 'Send a test')),
        main.error ? h('div', { class: 'error', role: 'alert' }, main.error) : null),
      h('details', { class: 'where', open: !saved || null },
        h('summary', null, 'How to get a webhook link'),
        h('ol', { class: 'steps' },
          h('li', null, 'In Discord, click the gear next to the channel’s name (', h('b', null, 'Edit Channel'), ').'),
          h('li', null, 'Open ', h('b', null, 'Integrations'), ' and click ', h('b', null, 'Create Webhook'), ' (or ', h('b', null, 'Webhooks'), ', then ', h('b', null, 'New Webhook'), ').'),
          h('li', null, 'Click the new webhook, name it (for example “Game server”) and click ', h('b', null, 'Copy Webhook URL'), '.'),
          h('li', null, 'Paste it above, click ', h('b', null, 'Send a test'), ' and look in the channel. Then save.')),
        h('p', { class: 'small muted' }, 'Only people who manage the Discord server see Integrations: if you don’t, ask its owner for the link. Through the IP KVM, use its paste feature.')),
      h('div', { class: 'row' },
        h('button', { class: 'btn btn-primary', 'data-action': 'main-save', disabled: saving || link === saved }, icon('save'), saving ? 'Saving…' : 'Save'),
        link !== saved && saved ? h('button', { class: 'btn btn-ghost', 'data-action': 'main-undo' }, 'Undo') : null)));
}

function gamesCard(app) {
  const games = (app.status && app.status.games) || [];
  if (!games.length) return null;
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Where each game posts')),
    h('p', { class: 'text-2 small', style: { marginBottom: '8px' } }, 'Each game can have a channel of its own; change it on the game’s Settings tab.'),
    h('div', { class: 'list' }, games.map((g) => {
      const d = channels.get(g.id);
      const where = !d ? '…' : !d.on ? 'Doesn’t post in Discord' : d.webhook ? 'Its own channel' : 'The main channel';
      return h('div', { class: 'list-row', 'data-key': 'dc:' + g.id },
        tile(g),
        h('div', { class: 'grow' }, h('div', { class: 'name' }, g.short), h('div', { class: 'sub' }, where,
          d && d.on && g.discord && g.discord.error ? ` · ${g.discord.error}` : '')),
        h('a', { class: 'btn btn-sm', href: `#/game/${enc(g.id)}/settings` }, 'Settings'));
    })));
}

// ---------- starting games from Discord ----------

const okAddr = (a) => /^[\w.-]+(:\d+)?$/.test(a || '');
const wkKey = (name) => (name === 'botToken' ? 'token' : 'address');

function wkField(name, label, attrs, help) {
  return h('div', { class: 'field' }, h('label', { for: 'wk-' + name }, label),
    h('input', { class: `input${wkErrors[name] ? ' invalid' : ''}`, id: 'wk-' + name, value: wkForm[wkKey(name)] || '', spellcheck: 'false', autocomplete: 'off',
      oninput: (e) => { wkForm[wkKey(name)] = e.target.value; delete wkErrors[name]; if (name === 'jetkvm') delete wkErrors.key; appRef.render(); }, ...attrs }),
    help ? h('div', { class: 'help' }, help) : null,
    wkErrors[name] ? h('div', { class: 'error', role: 'alert' }, wkErrors[name]) : null);
}

// commandList names the commands friends can type, one per starred game.
function commandList() {
  const cmds = (wk && wk.commands) || [];
  if (!cmds.length) return h('span', null, 'no command yet: star a game and its command appears');
  return cmds.map((c, i) => [i ? ', ' : '', h('b', null, '/' + c.command)]);
}

function wakePrereqs(st, on) {
  const c = (st && st.checklist) || {};
  const out = [];
  const mac = isMac(st);
  if (!c.wired) {
    out.push(callout('warn', 'alert', h('p', null, h('strong', null, 'Connect this computer to the router with a network cable. '), 'Wake-on-LAN doesn’t work over Wi-Fi.')));
  } else if (!c.wakeOnLan) {
    out.push(callout('warn', 'alert', h('p', null, mac ? '“Wake for network access” is off, so this Mac can’t be woken over the network yet.' : 'Windows doesn’t let the network adapter wake this computer yet.'),
      h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'enable-wol' }, icon('power'), mac ? 'Turn on Wake for network access' : 'Turn on Wake-on-LAN'))));
  }
  if (on && !c.firewall) {
    out.push(callout('warn', 'alert', h('p', null, `The ${mac ? 'macOS' : 'Windows'} firewall has to let the wake signal in, or the game won’t start when the computer wakes up.`),
      h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'fix-firewall' }, icon('shieldCheck'), mac ? 'Allow through the macOS firewall' : 'Allow through Windows Firewall'))));
  }
  return out;
}

function keyBox() {
  return [
    h('div', { class: 'keybox mono', id: 'wk-key' }, wk.key),
    h('div', { class: 'row', style: { gap: '8px', marginTop: '8px' } },
      h('button', { class: 'btn btn-sm', type: 'button', 'data-action': 'copy', 'data-text': wk.key, 'data-label': 'Key copied' }, icon('copy'), 'Copy the key'),
      wk.canPost ? h('button', { class: 'btn btn-sm', type: 'button', 'data-action': 'wk-post', 'data-what': 'key', disabled: !!wkBusy },
        icon('send'), wkBusy === 'post-key' ? 'Posting…' : 'Post it in Discord') : null),
  ];
}

function inviteRow() {
  return h('div', { class: 'row', style: { gap: '8px', marginTop: '10px' } },
    h('a', { class: 'btn btn-sm btn-primary', href: wk.invite, target: '_blank', rel: 'noopener noreferrer' }, icon('plus'), 'Add the bot to Discord'),
    h('button', { class: 'btn btn-sm', type: 'button', 'data-action': 'copy', 'data-text': wk.invite, 'data-label': 'Link copied' }, icon('copy'), 'Copy the link'),
    wk.canPost ? h('button', { class: 'btn btn-sm', type: 'button', 'data-action': 'wk-post', 'data-what': 'invite', disabled: !!wkBusy },
      icon('send'), wkBusy === 'post-invite' ? 'Posting…' : 'Post the link in Discord') : null);
}

function setupSteps() {
  const addr = (wkForm.address || '').trim();
  return h('ol', { class: 'setup-steps' },
    h('li', null, h('h3', null, 'Make a Discord bot'),
      h('ol', { class: 'steps' },
        h('li', null, 'Open ', h('a', { href: 'https://discord.com/developers/applications', target: '_blank', rel: 'noopener noreferrer' }, 'discord.com/developers/applications'),
          ', sign in with Discord and click ', h('b', null, 'New Application'), '. Call it, for example, “Game server”.'),
        h('li', null, 'Click ', h('b', null, 'Bot'), ' on the left, then ', h('b', null, 'Reset Token'), ', and copy the token. It’s the bot’s password: keep it to yourself.'),
        h('li', null, 'Paste it here. Nothing else there needs changing.')),
      wkField('botToken', 'Bot token', { type: 'password', placeholder: wk.token ? 'Saved. Paste a new one to change it.' : 'Paste the token' })),
    h('li', null, h('h3', null, 'Let the app into the JetKVM'),
      wkField('jetkvm', 'The JetKVM’s address', { placeholder: 'For example 192.168.1.50', style: { maxWidth: '260px' } }, 'It shows on the JetKVM’s little screen.'),
      h('ol', { class: 'steps' },
        h('li', null, 'Open the JetKVM’s page, as you do to use it, or ',
          okAddr(addr) ? h('a', { href: `http://${addr}/`, target: '_blank', rel: 'noopener noreferrer' }, 'open it on this computer') : 'open it on this computer', '.'),
        h('li', null, 'Go to ', h('b', null, 'Settings'), ', then ', h('b', null, 'Advanced'), ', and turn on ', h('b', null, 'Developer Mode'), '.'),
        h('li', null, 'Copy this key into ', h('b', null, 'SSH Public Key'), ' and click ', h('b', null, 'Update SSH Key'), ':')),
      ...keyBox(),
      wkErrors.key ? callout('bad', 'alert', h('p', null, wkErrors.key)) : null,
      h('p', { class: 'small muted', style: { marginTop: '8px' } }, 'Developer Mode lets whoever has this key into the JetKVM. Only this app has it: it stays on this computer.')),
    h('li', null, h('h3', null, 'Install the waker on the JetKVM'),
      h('p', { class: 'text-2' }, 'The app checks the bot token, copies the waker to the JetKVM and starts it. It starts by itself whenever the JetKVM does. This takes about a minute.'),
      h('div', { class: 'row', style: { marginTop: '10px' } },
        h('button', { class: 'btn btn-primary', type: 'button', 'data-action': 'wk-install', disabled: !!wkBusy || wk.busy || !!wk.missing },
          icon('download'), wkBusy === 'install' ? 'Installing…' : 'Install on the JetKVM')),
      wk.missing ? h('p', { class: 'small muted', style: { marginTop: '8px' } }, wk.missing) : null));
}

function wakerStatus() {
  const st = wk.status;
  const bot = h('b', null, wk.bot || (st && st.bot) || 'The bot');
  let top;
  if (wkChecking && !st && !wk.problem) top = callout('info', 'clock', h('p', null, 'Asking the JetKVM how the waker is doing…'));
  else if (wk.problem) {
    top = callout('warn', 'alert', h('p', null, wk.problem),
      wk.action === 'key' ? h('div', { style: { marginTop: '10px' } }, ...keyBox()) : null,
      wk.action === 'trust' ? [h('p', { class: 'small text-2' }, 'Only trust it if you reset or updated your JetKVM.'),
        h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'wk-install', 'data-trust': '1', disabled: !!wkBusy }, icon('shieldCheck'), 'Trust it and install again'))] : null);
  } else if (st && st.connected) {
    top = callout('good', 'check', h('p', null, bot, ' is in Discord. Friends type ', commandList(), ' in each game’s channel to wake this computer and start that game.'),
      st.lastWake ? h('p', { class: 'small text-2' }, `Last used ${ago(st.lastWake)}${st.wokenBy ? ' by ' + st.wokenBy : ''}.`) : null);
  } else if (st && st.error) top = callout('warn', 'alert', h('p', null, bot, ' can’t get into Discord: ', st.error));
  else top = callout('info', 'clock', h('p', null, 'The waker is starting on the JetKVM…'));
  const notInDiscord = st && st.connected && !st.servers && wk.invite;
  return [
    top,
    notInDiscord ? callout('warn', 'alert', h('p', null, h('strong', null, 'Last step: add the bot to your Discord server. '), 'Someone who can manage the Discord server has to do it.'), inviteRow()) : null,
    h('p', { class: 'small muted', style: { marginTop: '12px' } }, [wk.address ? `JetKVM at ${wk.address}` : '', wk.checkedAt ? `Checked ${ago(wk.checkedAt)}` : ''].filter(Boolean).join(' · ')),
    !notInDiscord && wk.invite ? h('details', { class: 'where' }, h('summary', null, 'Add the bot to Discord again'), inviteRow()) : null,
    h('details', { class: 'where' }, h('summary', null, 'Change the bot token or the JetKVM’s address'),
      h('div', { class: 'form', style: { marginTop: '4px' } },
        wkField('botToken', 'New bot token', { type: 'password', placeholder: 'Leave empty to keep the one saved' }),
        wkField('jetkvm', 'The JetKVM’s address', { style: { maxWidth: '260px' } }),
        h('div', { class: 'row' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'wk-install', disabled: !!wkBusy || wk.busy }, icon('download'), wkBusy === 'install' ? 'Installing…' : 'Install again')))),
    h('div', { class: 'row', style: { marginTop: '14px', paddingTop: '12px', borderTop: '1px solid var(--line)' } },
      h('button', { class: 'btn btn-sm', 'data-action': 'wk-check', disabled: wkChecking }, icon('refresh'), wkChecking ? 'Checking…' : 'Check again'),
      h('button', { class: 'btn btn-sm btn-danger', 'data-action': 'wk-remove', disabled: !!wkBusy || wk.busy }, icon('trash'), wkBusy === 'remove' ? 'Turning off…' : 'Turn off')),
  ];
}

function wakerCard(app) {
  return h('section', { class: 'card', id: 'discord-start' },
    h('div', { class: 'card-head' }, icon('power'), h('h2', null, 'Start games from Discord'), wk && wk.on ? h('span', { class: 'badge good' }, 'On') : null),
    h('p', { class: 'text-2', style: { marginBottom: '12px' } }, 'Each starred game gets its own command in Discord, named after the game, that works in that game’s channel. ',
      'If this computer is asleep it wakes up, and only that game starts. A small helper on your JetKVM, which is always on, does the waking.',
      wk && (wk.commands || []).length ? [' Right now: ', commandList(), '.'] : null),
    !wk ? h('p', { class: 'muted' }, 'Loading…') : [
      wk.busy && !wkBusy ? callout('info', 'clock', h('p', null, 'The app is updating the waker on the JetKVM…')) : null,
      ...wakePrereqs(app.status, wk.on),
      wk.on ? wakerStatus() : setupSteps(),
    ]);
}

export default {
  title: 'Discord',
  render(app) {
    appRef = app;
    return h('div', { class: 'stack' },
      main.loaded ? mainCard() : h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading…')),
      gamesCard(app),
      wakerCard(app));
  },
  mount(app) {
    appRef = app;
    main.loaded = false;
    loadMain();
    loadChannels();
    loadWaker(true);
    const A = app.actions;
    A['main-undo'] = () => { main.webhook = (settings && settings.settings && settings.settings.discordWebhook) || ''; main.error = ''; app.render(); };
    A['main-test'] = async () => {
      testing = true;
      app.render();
      try {
        await api('/api/discord/test', { method: 'POST', body: { webhook: main.webhook.trim() } });
        main.error = '';
        toast('Test message sent. Look in the Discord channel.', 'good');
      } catch (e) {
        const f = (e.data && e.data.fields) || {};
        main.error = f.discordWebhook || f.webhook || e.message;
        toast(main.error, 'bad', 10000);
      } finally {
        testing = false;
        app.render();
      }
    };
    A['main-save'] = async () => {
      saving = true;
      app.render();
      try {
        await api('/api/discord/main', { method: 'POST', body: { webhook: main.webhook.trim() } });
        main.error = '';
        toast(main.webhook.trim() ? 'Saved. Games without their own channel post there.' : 'Saved. Games without their own channel don’t post now.', 'good');
        main.loaded = false;
        await loadMain();
      } catch (e) {
        const f = (e.data && e.data.fields) || {};
        main.error = f.webhook || f.discordWebhook || e.message;
        toast(main.error, 'bad', 10000);
      } finally {
        saving = false;
        app.render();
        app.poll();
      }
    };
    A['wk-install'] = async (d) => {
      const trust = d.trust === '1' || d.trust === 'yes';
      if (d.trust === '1' && !await confirmDialog({ title: 'Trust this JetKVM?', message: 'Its identity changed. That’s expected after it’s reset or its system is updated. Only go on if that’s what happened: otherwise another device may be answering at its address.', confirm: 'Trust it' })) return;
      wkBusy = 'install';
      wkErrors = {};
      app.render();
      try {
        wk = await api('/api/jetkvm/install', { method: 'POST', body: { address: (wkForm.address || '').trim(), token: (wkForm.token || '').trim(), trust } });
        wkForm.token = '';
        const st = wk.status;
        toast(st && st.connected && !st.servers ? 'Installed. Last step: add the bot to your Discord server.' : 'Installed. Friends can use the starred games’ commands in Discord.', 'good', 8000);
        app.poll();
      } catch (e) {
        const data = e.data || {};
        wkErrors = { ...(data.fields || {}) };
        if (data.action === 'key') wkErrors.key = e.message;
        if (data.action === 'trust' && !trust) {
          wkBusy = '';
          app.render();
          if (await confirmDialog({ title: 'The JetKVM’s identity changed', message: e.message + ' Trust it only if you reset or updated your JetKVM.', confirm: 'Trust it' })) {
            return await A['wk-install']({ trust: 'yes' });
          }
          return;
        }
        toast(data.fields ? Object.values(data.fields)[0] : e.message, 'bad', 12000);
      } finally {
        wkBusy = '';
        app.render();
      }
    };
    A['wk-check'] = () => loadWaker(true);
    A['wk-post'] = async (d) => {
      wkBusy = 'post-' + d.what;
      app.render();
      try {
        await api('/api/jetkvm/post', { method: 'POST', body: { what: d.what } });
        toast(d.what === 'key' ? 'Posted. Copy the key from the Discord channel on your own computer.' : 'Posted. Open the link from the Discord channel.', 'good', 8000);
      } catch (e) {
        toast(e.message, 'bad', 9000);
      } finally {
        wkBusy = '';
        app.render();
      }
    };
    A['wk-remove'] = async () => {
      if (!await confirmDialog({ title: 'Turn off starting from Discord?', message: 'The waker is taken off the JetKVM, and the games’ commands stop working. The bot stays in your Discord server until you remove it there.', confirm: 'Turn off', kind: 'danger' })) return;
      const remove = async (force) => {
        wkBusy = 'remove';
        app.render();
        try {
          await api('/api/jetkvm/remove', { method: 'POST', body: { force } });
          toast('Starting games from Discord is off.', 'good');
          await loadWaker(false);
          app.poll();
        } catch (e) {
          wkBusy = '';
          if (!force && e.data && e.data.action === 'force') {
            if (await confirmDialog({ title: 'Turn it off here anyway?', message: e.message + ' If you turn it off here, the waker on the JetKVM keeps waking this computer when someone uses a command, but no game starts. To stop the bot, reset its token in the Discord Developer Portal.', confirm: 'Turn off here', kind: 'danger' })) {
              return await remove(true);
            }
          } else {
            toast(e.message, 'bad', 9000);
          }
        } finally {
          wkBusy = '';
          app.render();
        }
      };
      await remove(false);
    };
  },
  unmount() {
    clearTimeout(wkTimer);
  },
};
