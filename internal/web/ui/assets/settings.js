// Settings: one form for the server, automation, resting and waking up,
// backups and folders, plus maintenance.

import { h, icon, api, toast, confirmDialog, toggle, ago } from './dom.js';

let view = null;    // last loaded settings response
let draft = null;   // edited copy of view.settings
let errors = {};
let saving = false;
let testing = false;
let checking = false;
let appRef = null;

// Starting the server from Discord, through the waker on the JetKVM. Its
// fields aren't part of the settings form: they're used when installing.
let wk = null;                        // GET /api/jetkvm
let wkForm = { address: '', token: '' };
let wkErrors = {};
let wkBusy = '';                      // what's under way, for its button
let wkChecking = false;               // asking the JetKVM how the waker is

async function loadWaker(live) {
  wkChecking = live;
  if (appRef && appRef.page.id === 'settings') appRef.render();
  try {
    wk = await api('/api/jetkvm' + (live ? '?live=1' : ''));
    if (!wkForm.address && wk.address) wkForm.address = wk.address;
  } catch (e) {
    toast(e.message, 'bad');
  } finally {
    wkChecking = false;
  }
  if (appRef && appRef.page.id === 'settings') appRef.render();
}

async function load() {
  try {
    view = await api('/api/settings');
    draft = structuredClone(view.settings);
    errors = {};
  } catch (e) {
    toast(e.message, 'bad');
  }
  if (appRef && appRef.page.id === 'settings') appRef.render();
}

const dirty = () => view && draft && JSON.stringify(draft) !== JSON.stringify(view.settings);

function input(name, attrs = {}) {
  return h('input', { class: `input${errors[name] ? ' invalid' : ''}`, 'data-field': name, value: draft[name] ?? '', id: 'f-' + name, ...attrs });
}

function fieldFor(name, label, el, help) {
  return h('div', { class: 'field' }, h('label', { for: 'f-' + name }, label), el, help ? h('div', { class: 'help' }, help) : null,
    errors[name] ? h('div', { class: 'error', role: 'alert' }, errors[name]) : null);
}

function switchRow(name, title, help, opts = {}) {
  return h('div', { class: 'toggle-row' },
    h('div', { class: 'grow' }, h('div', { class: 'title' }, title), help ? h('div', { class: 'help' }, help) : null, opts.extra || null,
      errors[name] ? h('div', { class: 'error' }, errors[name]) : null),
    toggle({ 'data-field': name, checked: !!draft[name], 'aria-label': title, disabled: opts.disabled }));
}

function serverSection() {
  const pwId = 'f-password';
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Server')),
    h('div', { class: 'form' },
      h('div', { class: 'field-row' },
        fieldFor('serverName', 'Server name', input('serverName', { maxlength: 64 }), 'Shown in Valheim’s server list.'),
        fieldFor('password', 'Server password',
          h('div', { class: 'input-group' },
            input('password', { type: 'text', maxlength: 64, autocomplete: 'off', spellcheck: 'false', id: pwId }),
            h('button', { class: 'btn', type: 'button', 'data-action': 'settings-genpw', title: 'Make up a new password' }, icon('sparkle'), 'New')),
          'Friends type this when they join. At least 5 characters. It can’t be part of the server or world name.')),
      switchRow('crossplay', 'Crossplay (recommended)',
        'Lets friends on Xbox, PlayStation, Switch 2 and Game Pass join, and gives you a join code. Joining by code works without setting up your router.'),
      switchRow('public', 'Show in the public server list',
        'Anyone can find the server by name in Valheim (they still need the password). Leave off to keep it private.'),
      h('details', null, h('summary', { style: { cursor: 'pointer' } }, 'Network port'),
        h('div', { style: { marginTop: '12px', maxWidth: '260px' } },
          fieldFor('port', 'Game port', input('port', { type: 'number', min: 1024, max: 65534 }),
            'Valheim uses this port and the next one (UDP). 2456 is normal; only change it if something else uses it.')))));
}

function discordSection() {
  const link = (draft.discordWebhook || '').trim();
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Discord')),
    switchRow('discord', 'Post the join code in Discord',
      'Each time the server is online, its join code is posted in a Discord channel, so friends can join without asking for it.'),
    draft.discord ? h('div', { class: 'form discord-form' },
      fieldFor('discordWebhook', 'The channel’s webhook link',
        h('div', { class: 'input-group' },
          input('discordWebhook', { placeholder: 'https://discord.com/api/webhooks/…', spellcheck: 'false', autocomplete: 'off' }),
          h('button', { class: 'btn', type: 'button', 'data-action': 'discord-test', disabled: testing || !link },
            icon('send'), testing ? 'Sending…' : 'Send a test'))),
      h('details', { class: 'where', open: !link },
        h('summary', null, 'How to get the webhook link'),
        h('ol', { class: 'steps' },
          h('li', null, 'In Discord, click the gear next to the channel’s name (', h('b', null, 'Edit Channel'), ').'),
          h('li', null, 'Open ', h('b', null, 'Integrations'), ' and click ', h('b', null, 'Create Webhook'), ' (or ', h('b', null, 'Webhooks'), ', then ', h('b', null, 'New Webhook'), ').'),
          h('li', null, 'Click the new webhook, name it (for example “Valheim”) and click ', h('b', null, 'Copy Webhook URL'), '.'),
          h('li', null, 'Paste it above, click ', h('b', null, 'Send a test'), ' and look in the channel. Then save.')),
        h('p', { class: 'small muted' },
          'Only people who manage the Discord server see Integrations: if you don’t, ask its owner for the link. Through the IP KVM, use its paste feature, or open Discord in a browser on this computer and copy the link there.')),
      switchRow('discordPassword', 'Include the password',
        'Hidden as a spoiler until it’s clicked. Turning it on while the server is online posts the message again with it. Only turn this on if the channel is just for your friends.'),
      switchRow('discordStopped', 'Also post when the server stops',
        'So nobody tries a join code that no longer works.'),
      switchRow('discordDaily', 'Post who died the most each day',
        'At 23:59, if anyone played that day. If the computer is off or resting then, it’s posted once it’s back.')) : null);
}

const okAddr = (a) => /^[\w.\-]+(:\d+)?$/.test(a || '');

// The form's fields are named as the app names them in its errors.
const wkKey = (name) => (name === 'botToken' ? 'token' : 'address');

function wkField(name, label, attrs, help) {
  return h('div', { class: 'field' }, h('label', { for: 'wk-' + name }, label),
    h('input', { class: `input${wkErrors[name] ? ' invalid' : ''}`, id: 'wk-' + name, 'data-wk': name, value: wkForm[wkKey(name)] || '', spellcheck: 'false', autocomplete: 'off', ...attrs }),
    help ? h('div', { class: 'help' }, help) : null,
    wkErrors[name] ? h('div', { class: 'error', role: 'alert' }, wkErrors[name]) : null);
}

// wakePrereqs are what this computer needs for Wake-on-LAN to work.
function wakePrereqs(app, on) {
  const st = app.status;
  if (!st) return [];
  const c = st.checklist;
  const out = [];
  if (!c.wired) {
    out.push(h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, h('strong', null, 'Connect this computer to the router with a network cable. '), 'Wake-on-LAN doesn’t work over Wi-Fi.'))));
  } else if (!c.wakeOnLan) {
    out.push(h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, 'Windows doesn’t let the network adapter wake this computer yet.'),
      h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'enable-wol' }, icon('power'), 'Turn on Wake-on-LAN')))));
  }
  if (draft.restMode !== 'sleep') {
    out.push(h('div', { class: 'callout info' }, icon('info'), h('div', { class: 'body' },
      h('p', null, 'Choose ', h('b', null, 'Sleep'), ' under ', h('b', null, 'Resting and waking up'), ': most computers, Macs included, can’t be woken over the network from hibernation.'))));
  }
  if (on && !c.firewall) {
    out.push(h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, 'Windows Firewall has to let the wake signal in, or the server won’t start when the computer wakes up.'),
      h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'fix-firewall' }, icon('shieldCheck'), 'Allow through Windows Firewall')))));
  }
  return out;
}

function keyBox(showPost) {
  return [
    h('div', { class: 'keybox mono', id: 'wk-key' }, wk.key),
    h('div', { class: 'row', style: { gap: '8px', marginTop: '8px' } },
      h('button', { class: 'btn btn-sm', type: 'button', 'data-action': 'copy', 'data-text': wk.key, 'data-label': 'Key copied' }, icon('copy'), 'Copy the key'),
      showPost && wk.canPost ? h('button', { class: 'btn btn-sm', type: 'button', 'data-action': 'wk-post', 'data-what': 'key', disabled: !!wkBusy },
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

function wakeSetupSteps() {
  const addr = (wkForm.address || '').trim();
  const portal = 'https://discord.com/developers/applications';
  return h('ol', { class: 'setup-steps' },
    h('li', null,
      h('h3', null, 'Make a Discord bot'),
      h('ol', { class: 'steps' },
        h('li', null, 'Open ', h('a', { href: portal, target: '_blank', rel: 'noopener noreferrer' }, 'discord.com/developers/applications'),
          ', sign in with Discord and click ', h('b', null, 'New Application'), '. Call it, for example, “Valheim”.'),
        h('li', null, 'Click ', h('b', null, 'Bot'), ' on the left, then ', h('b', null, 'Reset Token'), ', and copy the token. It’s the bot’s password: keep it to yourself.'),
        h('li', null, 'Paste it here. Nothing else there needs changing.')),
      wkField('botToken', 'Bot token', { type: 'password', placeholder: wk.token ? 'Saved. Paste a new one to change it.' : 'Paste the token' }),
      h('p', { class: 'small muted' }, 'Through the IP KVM, use its paste feature, or open the page in a browser on this computer and copy the token there.')),
    h('li', null,
      h('h3', null, 'Let the app into the JetKVM'),
      wkField('jetkvm', 'The JetKVM’s address', { placeholder: 'For example 192.168.1.50', style: { maxWidth: '260px' } }, 'It shows on the JetKVM’s little screen.'),
      h('ol', { class: 'steps' },
        h('li', null, 'Open the JetKVM’s page, as you do to use it, or ',
          okAddr(addr) ? h('a', { href: `http://${addr}/`, target: '_blank', rel: 'noopener noreferrer' }, 'open it on this computer') : 'open it on this computer', '.'),
        h('li', null, 'Go to ', h('b', null, 'Settings'), ', then ', h('b', null, 'Advanced'), ', and turn on ', h('b', null, 'Developer Mode'), '.'),
        h('li', null, 'Copy this key into ', h('b', null, 'SSH Public Key'), ' and click ', h('b', null, 'Update SSH Key'), ':')),
      ...keyBox(true),
      wkErrors.key ? h('div', { class: 'callout bad', role: 'alert' }, icon('alert'), h('div', { class: 'body' }, h('p', null, wkErrors.key))) : null,
      h('p', { class: 'small muted', style: { marginTop: '8px' } }, 'Developer Mode lets whoever has this key into the JetKVM. Only this app has it: it stays on this computer.')),
    h('li', null,
      h('h3', null, 'Install the waker on the JetKVM'),
      h('p', { class: 'text-2' }, 'The app checks the bot token, copies the waker to the JetKVM and starts it. It starts by itself whenever the JetKVM does. This takes about a minute.'),
      h('div', { class: 'row', style: { marginTop: '10px' } },
        h('button', { class: 'btn btn-primary', type: 'button', 'data-action': 'wk-install', disabled: !!wkBusy || wk.busy || !!wk.missing },
          icon('download'), wkBusy === 'install' ? 'Installing…' : 'Install on the JetKVM')),
      wk.missing ? h('p', { class: 'small muted', style: { marginTop: '8px' } }, wk.missing) : null));
}

function wakeStatus() {
  const st = wk.status;
  const bot = h('b', null, wk.bot || (st && st.bot) || 'The bot');
  let main;
  if (wkChecking && !st && !wk.problem) {
    main = h('div', { class: 'callout info' }, icon('clock'), h('div', { class: 'body' }, h('p', null, 'Asking the JetKVM how the waker is doing…')));
  } else if (wk.problem) {
    main = h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, wk.problem),
      wk.action === 'key' ? h('div', { style: { marginTop: '10px' } }, ...keyBox(true)) : null,
      wk.action === 'trust' ? [
        h('p', { class: 'small text-2' }, 'Only trust it if you reset or updated your JetKVM.'),
        h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'wk-install', 'data-trust': '1', disabled: !!wkBusy },
          icon('shieldCheck'), wkBusy === 'install' ? 'Installing…' : 'Trust it and install again')),
      ] : null));
  } else if (st && st.connected) {
    main = h('div', { class: 'callout good' }, icon('check'), h('div', { class: 'body' },
      h('p', null, bot, ' is in Discord. Friends type ', h('b', null, '/valheim'), ' there to wake this computer and start the server.'),
      st.lastWake ? h('p', { class: 'small text-2' }, `Last used ${ago(st.lastWake)}${st.wokenBy ? ' by ' + st.wokenBy : ''}.`) : null));
  } else if (st && st.error) {
    main = h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, bot, ' can’t get into Discord: ', st.error)));
  } else {
    main = h('div', { class: 'callout info' }, icon('clock'), h('div', { class: 'body' },
      h('p', null, 'The waker is starting on the JetKVM…')));
  }
  const notInDiscord = st && st.connected && !st.servers && wk.invite;
  return [
    main,
    notInDiscord ? h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, h('strong', null, 'Last step: add the bot to your Discord server. '),
        wk.guild ? 'The link only offers the Discord server of your join-code channel.' : 'Someone who can manage the Discord server has to do it.'),
      inviteRow())) : null,
    h('p', { class: 'small muted', style: { marginTop: '12px' } }, [
      wk.address ? `JetKVM at ${wk.address}` : '',
      wk.guild ? 'Answers only in the Discord server of the join-code channel' : '',
      wk.checkedAt ? `Checked ${ago(wk.checkedAt)}` : '',
    ].filter(Boolean).join(' · ')),
    !wk.guild ? h('p', { class: 'small muted' }, 'The bot answers /valheim in any Discord server it’s added to. Turn on “Post the join code in Discord” above to keep it to yours.') : null,
    !notInDiscord && wk.invite ? h('details', { class: 'where' }, h('summary', null, 'Add the bot to Discord again'), inviteRow()) : null,
    h('details', { class: 'where' }, h('summary', null, 'Change the bot token or the JetKVM’s address'),
      h('div', { class: 'form', style: { marginTop: '4px' } },
        wkField('botToken', 'New bot token', { type: 'password', placeholder: 'Leave empty to keep the one saved' }),
        wkField('jetkvm', 'The JetKVM’s address', { style: { maxWidth: '260px' } }),
        h('div', { class: 'row' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'wk-install', disabled: !!wkBusy || wk.busy },
          icon('download'), wkBusy === 'install' ? 'Installing…' : 'Install again')))),
    h('div', { class: 'row', style: { marginTop: '14px', paddingTop: '12px', borderTop: '1px solid var(--line)' } },
      h('button', { class: 'btn btn-sm', 'data-action': 'wk-check', disabled: wkChecking || wk.busy }, icon('refresh'), wkChecking ? 'Checking…' : 'Check again'),
      h('button', { class: 'btn btn-sm btn-danger', 'data-action': 'wk-remove', disabled: !!wkBusy || wk.busy }, icon('trash'), wkBusy === 'remove' ? 'Turning off…' : 'Turn off')),
  ];
}

function discordStartSection(app) {
  return h('section', { class: 'card', id: 'discord-start' },
    h('div', { class: 'card-head' }, h('h2', null, 'Start the server from Discord'),
      wk && wk.on ? h('span', { class: 'badge good' }, 'On') : null),
    h('p', { class: 'text-2', style: { marginBottom: '12px' } }, 'Friends type ', h('b', null, '/valheim'),
      ' in your Discord. If this computer is asleep, it wakes up, and the server starts. A small helper on your JetKVM, which is always on, does the waking.'),
    !wk ? h('p', { class: 'muted' }, 'Loading…') : [
      wk.busy && !wkBusy ? h('div', { class: 'callout info' }, icon('clock'), h('div', { class: 'body' }, h('p', null, 'The app is updating the waker on the JetKVM…'))) : null,
      ...wakePrereqs(app, wk.on),
      wk.on ? wakeStatus() : wakeSetupSteps(),
    ]);
}

function automationSection() {
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Automatic')),
    switchRow('updateOnStart', 'Update the server before it starts',
      'Friends can’t join if their Valheim is newer than the server, so this checks Steam for updates each time.'),
    switchRow('autoRestart', 'Restart the server if it crashes', 'Gives up after 3 crashes in 15 minutes.'),
    switchRow('startWithApp', 'Start the server when this app opens'),
    switchRow('startAfterWake', 'Start the server again after the computer wakes up',
      'If the server was running when the computer went to rest, it starts again by itself when someone wakes the computer.'),
    switchRow('idleHibernate', draft.restMode === 'sleep' ? 'Sleep when nobody is playing' : 'Hibernate when nobody is playing',
      'Saves power. Wake the computer with Wake-on-LAN (for example from the IP KVM) or its power button. After waking up, it stays awake for at least 15 minutes.', {
      extra: draft.idleHibernate ? h('div', { class: 'row', style: { marginTop: '8px' } }, 'After',
        h('input', { class: `input${errors.idleMinutes ? ' invalid' : ''}`, type: 'number', min: 10, max: 1440, 'data-field': 'idleMinutes', value: draft.idleMinutes, style: { width: '96px' }, 'aria-label': 'Minutes' }),
        'minutes without players') : null,
    }),
    h('div', { class: 'toggle-row' },
      h('div', { class: 'grow' }, h('div', { class: 'title' }, 'Start this app when Windows starts'),
        h('div', { class: 'help' }, 'Opens quietly in the background (look for the shield in the taskbar corner).')),
      toggle({ checked: !!view.launchOnLogin, 'data-action': 'settings-login', 'aria-label': 'Start with Windows' })));
}

function restSection(app) {
  const st = app.status;
  const canSleep = !st || st.sleep.available;
  const option = (value, title, help, disabled) => h('label', { class: `choice${draft.restMode === value ? ' on' : ''}${disabled ? ' disabled' : ''}` },
    h('input', { type: 'radio', name: 'restMode', value, 'data-field': 'restMode', checked: draft.restMode === value, disabled }),
    h('div', null, h('div', { class: 'title' }, title), h('div', { class: 'help' }, help)));
  const wired = (st ? st.network : []).filter((a) => a.wired);
  const main = wired.find((a) => a.connected);
  const others = wired.filter((a) => a !== main);
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Resting and waking up')),
    h('p', { class: 'text-2', style: { marginBottom: '12px' } }, 'When the Hibernate (or Sleep) button is used, or nobody has played for a while, the computer can:'),
    h('div', { class: 'choices' },
      option('hibernate', 'Hibernate', 'Uses no power. Some computers can’t be woken by Wake-on-LAN from hibernation; if yours doesn’t wake, choose Sleep.'),
      option('sleep', 'Sleep', canSleep ? 'Uses a little power and wakes in seconds. Wake-on-LAN can wake most computers from sleep.' : st.sleep.detail, !canSleep)),
    h('h3', { class: 'subhead' }, 'Wake-on-LAN'),
    main ? [
      h('p', { class: 'text-2' }, 'To wake this computer from your IP KVM (or any Wake-on-LAN app), use this address:'),
      h('div', { class: 'row', style: { gap: '8px', margin: '8px 0' } },
        h('span', { class: 'mac mono' }, main.mac),
        h('button', { class: 'btn btn-sm', 'data-action': 'copy', 'data-text': main.mac, 'data-label': 'Address copied' }, icon('copy'), 'Copy')),
      h('p', { class: 'small muted' }, [main.name, main.description !== main.name ? main.description : '', (main.ipv4 || [])[0] ? 'IP ' + main.ipv4[0] : ''].filter(Boolean).join(' · ')),
      main.wakeArmed
        ? h('div', { class: 'callout good', style: { marginTop: '12px' } }, icon('check'), h('div', { class: 'body' },
          h('p', null, 'Windows lets this network adapter wake the computer.'),
          h('p', { class: 'small text-2' }, 'Test it once: use the Hibernate (or Sleep) button, wait a minute, then send Wake-on-LAN from the IP KVM.')))
        : h('div', { class: 'callout warn', style: { marginTop: '12px' } }, icon('alert'), h('div', { class: 'body' },
          h('p', null, 'Windows doesn’t let this network adapter wake the computer yet.'),
          h('div', { class: 'actions' }, h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'enable-wol' }, icon('power'), 'Turn on Wake-on-LAN')),
          h('p', { class: 'small text-2', style: { marginTop: '8px' } }, 'Windows asks for permission. Nobody playing is disconnected. If it still doesn’t wake afterwards, restart the computer once.'))),
      others.length ? h('p', { class: 'small muted', style: { marginTop: '10px' } }, 'Other network ports: ',
        others.map((a) => `${a.name} (${a.mac}${a.connected ? '' : ', no cable'})`).join(', ')) : null,
    ] : h('div', { class: 'callout warn' }, icon('alert'), h('div', { class: 'body' },
      h('p', null, h('strong', null, 'Wake-on-LAN needs a network cable. ')),
      h('p', null, 'No wired network connection was found. Connect the computer to the router with an Ethernet cable: Wake-on-LAN doesn’t work over Wi-Fi.'))));
}

function backupSection() {
  const num = (name, attrs) => input(name, { type: 'number', ...attrs });
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Saving & backups')),
    h('div', { class: 'field-row' },
      fieldFor('saveIntervalMinutes', 'Save the world every', h('div', { class: 'input-group' }, num('saveIntervalMinutes', { min: 1, max: 240 }), h('span', { class: 'muted', style: { alignSelf: 'center' } }, 'minutes')),
        'More often means less lost if the computer loses power. Each save causes a tiny hiccup.'),
      fieldFor('backups', 'Automatic backups to keep', num('backups', { min: 0, max: 50 }), 'Valheim’s own backups of the world.'),
      fieldFor('backupShortMinutes', 'First backup after', h('div', { class: 'input-group' }, num('backupShortMinutes', { min: 5, max: 1440 }), h('span', { class: 'muted', style: { alignSelf: 'center' } }, 'minutes'))),
      fieldFor('backupLongMinutes', 'Then a backup every', h('div', { class: 'input-group' }, num('backupLongMinutes', { min: 30, max: 10080 }), h('span', { class: 'muted', style: { alignSelf: 'center' } }, 'minutes')))));
}

function foldersSection() {
  const p = view.paths || {};
  const openBtn = (which) => h('button', { class: 'btn', type: 'button', 'data-action': 'open-folder', 'data-which': which }, icon('folder'), 'Open');
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Folders & advanced')),
    h('div', { class: 'form' },
      fieldFor('serverDir', 'Valheim server folder', h('div', { class: 'input-group' }, input('serverDir', { placeholder: p.server }), openBtn('server')),
        'Leave empty to use the app’s own folder. Point this at an existing install (for example one from the Steam client) to reuse it.'),
      fieldFor('saveDir', 'Save folder (worlds, admin lists)', h('div', { class: 'input-group' }, input('saveDir', { placeholder: p.save }), openBtn('saves')),
        'Leave empty to use the app’s own folder.'),
      fieldFor('extraArgs', 'Extra launch options', input('extraArgs', { placeholder: 'e.g. -instanceid 2', spellcheck: 'false' }),
        'Added to the server’s command line. Only for advanced setups.'),
      h('div', { class: 'row' },
        h('button', { class: 'btn btn-sm', 'data-action': 'open-folder', 'data-which': 'backups' }, icon('archive'), 'Backups folder'),
        h('button', { class: 'btn btn-sm', 'data-action': 'open-folder', 'data-which': 'logs' }, icon('terminal'), 'Logs folder'),
        h('button', { class: 'btn btn-sm', 'data-action': 'open-folder', 'data-which': 'data' }, icon('folder'), 'App folder'))));
}

// updatesSection is about this app updating itself from GitHub.
function updatesSection(app) {
  const a = (app.status && app.status.app) || {};
  let line;
  let button = null;
  if (!a.build) {
    line = `This copy (${view.version}) doesn’t update itself. Copies downloaded from GitHub do.`;
  } else if (a.updating) {
    line = `Updating to build ${a.latest ? a.latest.build : ''}… The panel reconnects by itself in a few seconds.`;
  } else if (a.failed > a.build && (!a.latest || a.latest.build === a.failed)) {
    line = `Build ${a.failed} didn’t start on this computer, so the app went back to build ${a.build}. It won’t install build ${a.failed} by itself.`;
    button = h('button', { class: 'btn btn-sm', 'data-action': 'app-update', 'data-install': '1' }, icon('refresh'), 'Try again');
  } else if (a.latest) {
    line = [h('strong', null, `Build ${a.latest.build} is ready`), a.latest.title ? `: ${a.latest.title}` : '.'];
    button = h('button', { class: 'btn btn-sm btn-primary', 'data-action': 'app-update', 'data-install': '1' }, icon('download'), 'Update now');
  } else if (a.error) {
    line = `Couldn’t check for updates: ${a.error}`;
  } else if (a.checkedAt) {
    line = `Up to date (build ${a.build}). Checked ${ago(a.checkedAt)}.`;
  } else {
    line = `Build ${a.build}. It looks for updates a couple of minutes after it starts.`;
  }
  if (a.build && !button && !a.updating) {
    button = h('button', { class: 'btn btn-sm', 'data-action': 'app-update', disabled: checking }, icon('refresh'), checking ? 'Checking…' : 'Check now');
  }
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'App updates')),
    a.build ? switchRow('autoUpdate', 'Update this app automatically',
      'Every hour it checks GitHub for a newer version and installs it when nothing is in progress. The Valheim server keeps running while the app restarts, so nobody is disconnected.') : null,
    h('div', { class: `row update-row${a.build ? ' ruled' : ''}` }, h('p', { class: 'grow text-2' }, line), button));
}

function maintenanceSection(app) {
  const st = app.status;
  const running = st && st.state !== 'stopped' && st.state !== 'not_installed';
  const busy = st && st.job && !st.job.finished;
  return h('section', { class: 'card' },
    h('div', { class: 'card-head' }, h('h2', null, 'Maintenance')),
    h('div', { class: 'stack', style: { gap: '12px' } },
      h('div', { class: 'row' },
        h('button', { class: 'btn', 'data-action': 'install', disabled: running || busy }, icon('refresh'), 'Update the server now'),
        h('button', { class: 'btn', 'data-action': 'repair', disabled: running || busy }, icon('wrench'), 'Repair server files'),
        running ? h('span', { class: 'small muted' }, 'Stop the server to update or repair it.') : null),
      h('div', { class: 'row' },
        h('button', { class: 'btn', 'data-action': 'fix-firewall' }, icon('shieldCheck'), 'Allow through Windows Firewall'),
        st && !st.hibernate.available ? h('button', { class: 'btn', 'data-action': 'enable-hibernate' }, icon('moon'), 'Turn on hibernation') : null),
      h('div', { class: 'row', style: { paddingTop: '12px', borderTop: '1px solid var(--line)' } },
        h('button', { class: 'btn', 'data-action': 'settings-quit', 'data-stop': '0' }, icon('x'), 'Close the app (server keeps running)'),
        running ? h('button', { class: 'btn btn-danger', 'data-action': 'settings-quit', 'data-stop': '1' }, icon('power'), 'Stop the server and close the app') : null),
      h('p', { class: 'small muted' }, `Valheim Easy Server ${view.version}`)));
}

export default {
  id: 'settings',
  title: 'Settings',
  icon: 'sliders',
  render(app) {
    appRef = app;
    if (!view || !draft) return h('div', { class: 'card' }, h('p', { class: 'muted' }, 'Loading settings…'));
    const changed = dirty();
    return h('div', { class: 'stack' },
      serverSection(),
      discordSection(),
      discordStartSection(app),
      automationSection(),
      restSection(app),
      backupSection(),
      foldersSection(),
      updatesSection(app),
      maintenanceSection(app),
      changed ? h('div', { class: 'savebar', role: 'region', 'aria-label': 'Unsaved changes' },
        h('span', null, 'You have unsaved changes.'),
        h('span', { class: 'spacer' }),
        h('button', { class: 'btn btn-ghost', 'data-action': 'settings-discard' }, 'Discard'),
        h('button', { class: 'btn btn-primary', 'data-action': 'settings-save', disabled: saving }, icon('save'), saving ? 'Saving…' : 'Save changes')) : null);
  },
  mount(app) {
    appRef = app;
    load().then(() => loadWaker(true));
    const A = app.actions;
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
        toast(st && st.connected && !st.servers ? 'Installed. Last step: add the bot to your Discord server.' : 'Installed. Friends can type /valheim in Discord.', 'good', 8000);
        app.poll();
      } catch (e) {
        const data = e.data || {};
        wkErrors = { ...(data.fields || {}) };
        if (data.action === 'key') wkErrors.key = e.message;
        if (data.action === 'trust' && !trust) {
          wkBusy = '';
          app.render();
          if (await confirmDialog({ title: 'The JetKVM’s identity changed', message: e.message + ' Trust it only if you reset or updated your JetKVM.', confirm: 'Trust it' })) {
            return await A['wk-install']({ trust: 'yes' }); // just confirmed
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
      if (!await confirmDialog({ title: 'Turn off starting from Discord?', message: 'The waker is taken off the JetKVM, and /valheim stops working. The bot stays in your Discord server until you remove it there.', confirm: 'Turn off', kind: 'danger' })) return;
      const remove = async (force) => {
        wkBusy = 'remove';
        app.render();
        try {
          await api('/api/jetkvm/remove', { method: 'POST', body: { force } });
          toast('Starting the server from Discord is off.', 'good');
          await loadWaker(false);
          app.poll();
        } catch (e) {
          wkBusy = '';
          if (!force && e.data && e.data.action === 'force') {
            if (await confirmDialog({ title: 'Turn it off here anyway?', message: e.message + ' If you turn it off here, the waker on the JetKVM keeps waking this computer when someone types /valheim, but the server won’t start. To stop the bot, reset its token in the Discord Developer Portal.', confirm: 'Turn off here', kind: 'danger' })) {
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
    A['settings-save'] = async () => {
      saving = true;
      app.render();
      try {
        const was = view.settings;
        const discordNew = draft.discord && (!was.discord || (draft.discordWebhook || '').trim() !== (was.discordWebhook || ''));
        await api('/api/settings', { method: 'POST', body: draft });
        const st = await api('/api/status').catch(() => null);
        const online = st && st.state === 'online';
        toast(online && st.needsRestart ? 'Saved. Restart the server to use the new settings.'
          : online && discordNew ? 'Saved. The join code is being posted in Discord.' : 'Settings saved.', 'good');
        await load();
      } catch (e) {
        errors = (e.data && e.data.fields) || {};
        toast(e.message, 'bad', 8000);
      } finally {
        saving = false;
        app.render();
      }
    };
    A['discord-test'] = async () => {
      testing = true;
      app.render();
      try {
        await api('/api/discord/test', { method: 'POST', body: { webhook: draft.discordWebhook || '' } });
        delete errors.discordWebhook;
        toast('Test message sent. Look in the Discord channel.', 'good');
      } catch (e) {
        const fields = (e.data && e.data.fields) || {};
        Object.assign(errors, fields);
        toast(fields.discordWebhook || e.message, 'bad', 10000);
      } finally {
        testing = false;
        app.render();
      }
    };
    A['app-update'] = async (d) => {
      const install = d.install === '1';
      checking = !install;
      app.render();
      try {
        await api('/api/app/update', { method: 'POST', body: { install } });
        if (install) {
          toast('Updating the app. The panel reconnects in a few seconds.', 'good', 8000);
        } else {
          const st = await api('/api/status');
          toast(st.app && st.app.latest ? `Build ${st.app.latest.build} is ready.` : 'The app is up to date.', 'good');
        }
      } catch (e) {
        toast(e.message, 'bad', 8000);
      } finally {
        checking = false;
        app.poll();
      }
    };
    A['settings-discard'] = () => { draft = structuredClone(view.settings); errors = {}; app.render(); };
    A['settings-genpw'] = () => {
      // The same words as the app's own (config.go): two words and a number.
      const words = ['odin', 'thor', 'freya', 'loki', 'frigg', 'tyr', 'baldr', 'heimdall', 'fjord', 'rune', 'skald', 'longship',
        'mead', 'troll', 'draugr', 'yggdrasil', 'bifrost', 'valkyrie', 'jotunn', 'mjolnir', 'raven', 'wolf', 'drakkar', 'saga',
        'bronze', 'silver', 'serpent', 'eikthyr'];
      const pick = (n) => crypto.getRandomValues(new Uint32Array(1))[0] % n;
      draft.password = `${words[pick(words.length)]}-${words[pick(words.length)]}-${1000 + pick(9000)}`;
      delete errors.password;
      app.render();
    };
    A['settings-login'] = async (d, el) => {
      try {
        await api('/api/system/launch-on-login', { method: 'POST', body: { enabled: el.checked } });
        view.launchOnLogin = el.checked;
        toast(el.checked ? 'The app will start with Windows.' : 'The app won’t start with Windows any more.', 'good');
      } catch (e) {
        el.checked = !el.checked;
        toast(e.message, 'bad');
      }
    };
    A['settings-quit'] = async (d) => {
      const stop = d.stop === '1';
      const ok = await confirmDialog({
        title: stop ? 'Stop the server and close the app?' : 'Close the app?',
        message: stop ? 'The world is saved, the server stops, and the app closes.' : 'The Valheim server keeps running. Open the app again to see it.',
        confirm: 'Close', kind: stop ? 'danger' : 'primary',
      });
      if (!ok) return;
      try { await api('/api/system/quit', { method: 'POST', body: { stopServer: stop } }); } catch { /* the app is closing */ }
      document.body.replaceChildren(h('div', { class: 'boot' }, h('img', { src: '/assets/logo.svg', width: 56, height: 56, alt: '' }),
        h('p', null, stop ? 'Saving the world and closing… You can close this window.' : 'The app has closed. You can close this window.')));
    };
    document.addEventListener('input', onInput);
    document.addEventListener('change', onInput);
    window.addEventListener('beforeunload', onLeave);
  },
  unmount() {
    document.removeEventListener('input', onInput);
    document.removeEventListener('change', onInput);
    window.removeEventListener('beforeunload', onLeave);
  },
};

function onLeave(e) {
  if (dirty()) { e.preventDefault(); e.returnValue = ''; }
}

function onInput(e) {
  const el = e.target;
  if (!draft || !el.dataset) return;
  if (el.dataset.wk) {
    const k = el.dataset.wk;
    wkForm[wkKey(k)] = el.value;
    delete wkErrors[k];
    if (k === 'jetkvm') delete wkErrors.key;
    appRef.render();
    return;
  }
  if (el.dataset.field) {
    const k = el.dataset.field;
    if (el.type === 'checkbox') draft[k] = el.checked;
    else if (el.type === 'number') draft[k] = el.value === '' ? 0 : Number(el.value);
    else draft[k] = el.value;
    delete errors[k];
    appRef.render();
  }
}

