// SPDX-License-Identifier: GPL-3.0-or-later
// Dependency-free regression harness for browser-local theme and language preferences.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.resolve(__dirname, '..');
const source = fs.readFileSync(path.join(root, 'internal/api/admin_assets/console.js'), 'utf8');
const LOCALES = {
  ru: JSON.parse(fs.readFileSync(path.join(root, 'internal/api/admin_assets/console.ru.json'))),
  en: JSON.parse(fs.readFileSync(path.join(root, 'internal/api/admin_assets/console.en.json'))),
};
const nodes = new Map();
const el = id => {
  if (!nodes.has(id)) nodes.set(id, {value:'',textContent:''});
  return nodes.get(id);
};
const store = new Map([
  ['opg3d_admin_theme', 'corrupt'],
  ['opg3d_admin_language', 'corrupt'],
]);
let reloads = 0;
const context = vm.createContext({
  LOCALES, el, all:()=>[],
  localStorage:{getItem:key=>store.get(key) ?? null,setItem:(key,value)=>store.set(key,value)},
  document:{documentElement:{dataset:{},lang:''}},
  window:{location:{reload:()=>{reloads++;}}},
});
const headStart = source.indexOf('  const preferenceKeys');
const headEnd = source.indexOf('  const esc', headStart);
vm.runInContext(source.slice(headStart, headEnd), context);
assert.equal(context.document.documentElement.dataset.theme, 'dark');
assert.equal(context.document.documentElement.lang, 'ru');
const formStart = source.indexOf('  el("uiTheme").value = theme;');
const formEnd = source.indexOf('  window.addEventListener("beforeunload"', formStart);
vm.runInContext(source.slice(formStart, formEnd), context);
assert.equal(el('uiTheme').value, 'dark');
assert.equal(el('uiLanguage').value, 'ru');
el('uiTheme').value = 'light';
el('uiTheme').oninput();
assert.equal(context.document.documentElement.dataset.theme, 'light');
el('uiLanguage').value = 'en';
el('settingsForm').onsubmit({preventDefault(){}});
assert.equal(store.get('opg3d_admin_theme'), 'light');
assert.equal(store.get('opg3d_admin_language'), 'en');
assert.equal(context.document.documentElement.lang, 'en');
assert.equal(el('settingsStatus').textContent, LOCALES.en.settingsSaved);
assert.equal(reloads, 1);
console.log('PASS: dark defaults, light preview, RU/EN preference persistence and reload');
