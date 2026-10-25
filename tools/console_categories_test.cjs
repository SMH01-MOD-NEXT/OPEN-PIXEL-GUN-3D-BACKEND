// SPDX-License-Identifier: GPL-3.0-or-later
// Exercise the actual category renderer without changing stock inventory types.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assets = path.join(__dirname, '../internal/api/admin_assets');
const source = fs.readFileSync(path.join(assets, 'console.js'), 'utf8');
const start = source.indexOf('  const category =');
const end = source.indexOf('  function drawItems(', start);
assert.ok(start >= 0 && end > start);
for (const lang of ['ru', 'en']) {
  const TEXT = JSON.parse(fs.readFileSync(path.join(assets, `console.${lang}.json`)));
  const context = vm.createContext({TEXT, t: key => TEXT[key]});
  vm.runInContext(source.slice(start, end) + '\nthis.category = category;', context);
  const avatar = {type:1080, index:601005, id:'avatar_mr_streamer', category:'Royale'};
  assert.equal(context.category(avatar), TEXT.royaleAvatarCategory);
  assert.equal(avatar.type, 1080);
  assert.equal(avatar.id, 'avatar_mr_streamer');
  for (const id of ['glider_aero_suit', 'royale_car_grave', 'shadow_trail']) {
    assert.equal(context.category({type:1080,id}), TEXT.types['1080']);
  }
  // The same tag also exists as parts: do not mislabel a part as a usable skin.
  assert.equal(context.category({type:1070,id:avatar.id}), TEXT.types['1070']);
  assert.equal(context.category({type:65,id:'1'}), TEXT.types['65']);
  assert.equal(context.category({type:1510,id:'skin_royale_hat_beast'}), TEXT.types['1510']);
  assert.equal(context.category({type:20,id:'Armor_Army_1'}), TEXT.types['20']);
  assert.equal(context.category({type:999,id:'unknown',category:'Unknown'}), 'Unknown');
  assert.ok(TEXT.types['1080'].toLowerCase().includes(lang === 'ru' ? 'скины' : 'skins'));
}
console.log('PASS: RU/EN avatar categories, mixed Royale items, parts and stock type preservation');
