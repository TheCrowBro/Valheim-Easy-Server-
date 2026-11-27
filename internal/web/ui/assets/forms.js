// Forms drawn from a game's settings schema (spec 2.9): each field says its
// kind (text, password, number, bool, choice, list), its label, help and
// limits, and whether it is asked in Add a game or kept under "More
// settings". Every value is text, as the app keeps them ("25565", "true").
//
// A form's state is { values: {key: text}, errors: {key: message} }, kept
// by the page that owns it (per instance), so the regular redraw never
// loses what was typed.

import { h, toggle } from './dom.js';

// fieldValue is the text a field shows: what was typed, else its value,
// else its default.
export const fieldValue = (f, values) => (values && values[f.key] != null ? String(values[f.key]) : f.default != null ? String(f.default) : '');

function errorFor(f, state, prefix) {
  const e = state.errors || {};
  return e[(prefix || '') + f.key] || e[f.key] || '';
}

function setter(f, state, onChange, prefix) {
  return (e) => {
    const el = e.target;
    state.values[f.key] = el.type === 'checkbox' ? String(el.checked) : el.value;
    if (state.errors) {
      delete state.errors[(prefix || '') + f.key];
      delete state.errors[f.key];
    }
    if (onChange) onChange(f.key);
  };
}

// fieldInput is one field's control and label.
export function fieldInput(f, state, opts = {}) {
  const id = `${opts.id || 'f'}-${f.key}`.replace(/[^\w-]/g, '_');
  const v = fieldValue(f, state.values);
  const err = errorFor(f, state, opts.prefix);
  const change = setter(f, state, opts.onChange, opts.prefix);
  const help = [f.help, f.restart && opts.running ? 'Used after the next restart.' : ''].filter(Boolean).join(' ');
  if (f.kind === 'bool') {
    return h('div', { class: 'toggle-row', 'data-key': 'field:' + f.key },
      h('div', { class: 'grow' }, h('label', { class: 'title', for: id }, f.label), help ? h('div', { class: 'help' }, help) : null,
        err ? h('div', { class: 'error', role: 'alert' }, err) : null),
      toggle({ id, checked: v === 'true', 'aria-label': f.label, onchange: change, disabled: opts.disabled }));
  }
  let input;
  const cls = `input${err ? ' invalid' : ''}`;
  if (f.kind === 'choice') {
    input = h('select', { class: cls, id, onchange: change, disabled: opts.disabled },
      (f.choices || []).map((c) => h('option', { value: c.value, selected: c.value === v }, c.label || c.value)));
  } else if (f.kind === 'list') {
    input = h('textarea', { class: cls, id, rows: 3, oninput: change, disabled: opts.disabled, spellcheck: 'false' }, v);
    input.value = v;
  } else {
    const attrs = { class: cls, id, value: v, oninput: change, disabled: opts.disabled, autocomplete: 'off' };
    if (f.kind === 'number') Object.assign(attrs, { type: 'number', min: f.min || null, max: f.max || null, inputmode: 'numeric' });
    else Object.assign(attrs, { type: 'text', maxlength: f.max || null, spellcheck: f.kind === 'password' ? 'false' : null });
    if (opts.placeholder) attrs.placeholder = opts.placeholder;
    input = h('input', attrs);
  }
  return h('div', { class: 'field', 'data-key': 'field:' + f.key },
    h('label', { for: id }, f.label),
    input,
    help ? h('div', { class: 'help' }, help) : null,
    err ? h('div', { class: 'error', role: 'alert' }, err) : null);
}

// schemaForm draws fields, the everyday ones first and the advanced ones
// under "More settings" (opened when one of them has a problem).
export function schemaForm(fields, state, opts = {}) {
  const basic = fields.filter((f) => !f.advanced);
  const more = fields.filter((f) => f.advanced);
  const open = more.some((f) => errorFor(f, state, opts.prefix));
  const draw = (list) => {
    // Short fields sit side by side; switches take a whole row each.
    const out = [];
    let row = [];
    const flush = () => { if (row.length) out.push(h('div', { class: 'field-row' }, row)); row = []; };
    for (const f of list) {
      if (f.kind === 'bool' || f.kind === 'list') {
        flush();
        out.push(fieldInput(f, state, opts));
      } else {
        row.push(fieldInput(f, state, opts));
      }
    }
    flush();
    return out;
  };
  return [
    ...draw(basic),
    more.length ? h('details', { class: 'more', open: open || null, 'data-key': 'more' },
      h('summary', null, opts.more || 'More settings'),
      h('div', { class: 'form' }, draw(more))) : null,
  ];
}

// changedValues are the values that differ from what is saved.
export function changedValues(fields, saved, values) {
  const out = {};
  for (const f of fields) {
    if (!(f.key in values)) continue;
    const was = fieldValue(f, saved);
    if (String(values[f.key]) !== was) out[f.key] = String(values[f.key]);
  }
  return out;
}

// summary writes a world's values as one line: "Difficulty: Normal · Game
// mode: Survival".
export function summary(fields, values) {
  const parts = [];
  for (const f of fields) {
    if (!values || values[f.key] == null || values[f.key] === '') continue;
    const v = String(values[f.key]);
    if (f.kind === 'bool') {
      if (v === 'true') parts.push(f.label);
      continue;
    }
    const c = (f.choices || []).find((x) => x.value === v);
    parts.push(`${f.label}: ${c ? c.label : v}`);
  }
  return parts.join(' · ');
}
