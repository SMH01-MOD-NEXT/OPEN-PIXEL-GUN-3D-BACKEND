// SPDX-License-Identifier: GPL-3.0-or-later
// Exercise the console's actual profile fill/save code without a browser login.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.resolve(__dirname, '..');
const source = fs.readFileSync(path.join(root, 'internal/api/admin_assets/console.js'), 'utf8');
const html = fs.readFileSync(path.join(root, 'internal/api/admin_assets/console.html'), 'utf8');
const nodes = new Map();
const el = id => {
  if (!nodes.has(id)) nodes.set(id, {value:'',textContent:'',classList:{toggle(){}}});
  return nodes.get(id);
};
let posted, failure;
const context = vm.createContext({renderSupportEvidence:()=>{},
  el, RAW:{}, selected:'test-player', dirty:false,
  player:{nick:'Test',profile:{level:7,experience:100,coins:20,gems:3,progress_slots:{'3':{PixelPassCurrency:600,BattlePassCurrency:345}}}},
  t:key=>key, status:()=>{}, base:id=>'/players/'+id, showPlayer:async()=>{},
  api:async(method,url,data)=>{posted={method,url,data:JSON.parse(JSON.stringify(data))};},
  mutate:(button,confirm,fn)=>{context.pending=fn().catch(e=>{failure=e;});},
});
const slice=(start,end)=>source.slice(source.indexOf(start),source.indexOf(end,source.indexOf(start)));
vm.runInContext(slice('  function fillPlayer()', '  function updateTarget()'), context);
vm.runInContext(slice('  el("profileForm").oninput', '  el("rawSection").onchange'), context);
vm.runInContext('fillPlayer()',context);
assert.equal(el('pfPixelPassCurrency').value,600);
assert.equal(el('pfBattlePassCurrency').value,345);
for (const lang of ['ru','en']) {
  const locale=JSON.parse(fs.readFileSync(path.join(root,`internal/api/admin_assets/console.${lang}.json`)));
  for (const key of ['pixelPassCurrency','battlePassCurrency','passCurrenciesHint']) assert.ok(locale[key]);
}
for (const id of ['pfPixelPassCurrency','pfBattlePassCurrency']) {
  assert.match(html,new RegExp(`id="${id}"[^>]*max="2147483647"`));
}
(async()=>{
  el('pfPixelPassCurrency').value='0';
  el('pfBattlePassCurrency').value='2147483647';
  el('profileForm').onsubmit({preventDefault(){}});
  await context.pending;
  assert.equal(failure,undefined);
  assert.deepEqual(posted,{method:'PUT',url:'/players/test-player/profile',data:{nick:'Test',level:7,experience:100,coins:20,gems:3,pixel_pass_currency:0,battle_pass_currency:2147483647}});
  for (const field of ['pfPixelPassCurrency','pfBattlePassCurrency']) {
    for (const value of ['-1','1.5','2147483648','NaN','Infinity']) {
      el('pfPixelPassCurrency').value='600';el('pfBattlePassCurrency').value='345';
      el(field).value=value;posted=undefined;failure=undefined;
      el('profileForm').onsubmit({preventDefault(){}});await context.pending;
      assert.ok(failure,`${field}=${value} must be rejected`);
      assert.equal(posted,undefined);
    }
  }
  context.player.profile={progress_document:{'3':{PixelPassCurrency:12,BattlePassCurrency:34}}};
  vm.runInContext('fillPlayer()',context);
  assert.equal(el('pfPixelPassCurrency').value,12);assert.equal(el('pfBattlePassCurrency').value,34);
  context.player.profile={};vm.runInContext('fillPlayer()',context);
  assert.equal(el('pfPixelPassCurrency').value,0);assert.equal(el('pfBattlePassCurrency').value,0);
  console.log('PASS: both pass currency fields, RU/EN labels, exact/zero/max balances, fallback reads and invalid input rejection');
})().catch(e=>{console.error(e);process.exitCode=1;});
