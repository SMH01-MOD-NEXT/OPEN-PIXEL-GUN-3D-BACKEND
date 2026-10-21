// SPDX-License-Identifier: GPL-3.0-or-later
// Dependency-free regression harness for the actual console rendering/form code.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.resolve(__dirname, '..');
const source = fs.readFileSync(path.join(root, 'internal/api/admin_assets/console.js'), 'utf8');
const TEXT = JSON.parse(fs.readFileSync(path.join(root, 'internal/api/admin_assets/console.ru.json')));
const nodes = new Map();
const el = id => {
 if (!nodes.has(id)) nodes.set(id, {value: '', hidden: false, disabled: false, textContent: '', innerHTML: '', querySelectorAll: () => [], showModal() { this.open = true; }, close() { this.open = false; }});
 return nodes.get(id);
};
let posted;
const context = vm.createContext({assert, TEXT, el, all:()=>[], discard:()=>true, report:e=>{throw e;}, status:()=>{}, base:id=>'/players/'+id,
 showPlayer:async()=>{}, api:async(method,url,body)=>{posted=body;if(method==='GET')return {revision:4,updated_at:'timestamp'};if(url.endsWith('/inventory/bulk/preview'))return {player_id:'test-player',action:body.action,changes:94,preserved_owned:0,skipped_equipped:0,skipped_unsupported:0,sample:[],plan_token:'preview-token'};return {changed:true,plan:{changes:94}};},
 category:item=>TEXT.types[String(item.type)],
 t:(key,values={})=>Object.entries(values).reduce((s,[k,v])=>s.split('{'+k+'}').join(String(v)),TEXT[key]||key),
 esc:value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))});
const slice=(start,end)=>source.slice(source.indexOf(start),source.indexOf(end,source.indexOf(start)));
vm.runInContext('let busy=false, selected="test-player", snapshot={revision:4,updated_at:"timestamp",items:[]}, player={nick:"Test"}, itemContext=null, bulkContext=null;'+
 slice('  function drawItems(', '  function renderOwned(')+
 slice('  function openItem(', '  function invalidateBulk('),context);
vm.runInContext(slice('  function invalidateBulk(', '  el("profileForm").oninput'),context);
const fox={type:80,index:20003,id:'pet_fox',name:'Fox',name_source:'derived_codename',editable:true};
context.fox=fox;
vm.runInContext('drawItems([fox],"catalogItems",false); openItem(fox,"grant");',context);
assert.match(el('catalogItems').innerHTML, /Fox/);
assert.match(el('catalogItems').innerHTML, /pet_fox/);
assert.match(el('catalogItems').innerHTML, /data-action="grant"/);
assert.ok(!el('catalogItems').innerHTML.includes(TEXT.readOnly));
assert.ok(el('catalogItems').innerHTML.includes(TEXT.derivedPetName));
assert.equal(el('itemLevel').value,1);
assert.equal(el('itemRarity').value,0);
assert.equal(el('quantityField').hidden,true);
assert.equal(el('levelField').hidden,false);
assert.equal(el('rarityField').hidden,true);
assert.equal(el('itemLevel').max,'65');
assert.equal(el('itemWarning').textContent,TEXT.petGrantWarning);
assert.equal(el('itemDialog').open,true);
(async()=>{
 el('itemLevel').value=35;
 el('itemForm').onsubmit({preventDefault(){}});
 await new Promise(resolve=>setImmediate(resolve));
 assert.deepEqual(posted && JSON.parse(JSON.stringify(posted)), {type:80,index:20003,action:'grant',quantity:1,level:35,rarity:0,revision:4,updated_at:'timestamp'});
 context.fox={...fox,quantity:1,level:27,equipped:true};
 vm.runInContext('drawItems([fox],"ownedItems",true);openItem(fox,"grant");',context);
 assert.match(el('ownedItems').innerHTML,/data-action="revoke" disabled/);
 assert.equal(el('itemLevel').value,27);
 vm.runInContext('openItem(fox,"revoke");',context);
 assert.equal(el('itemWarning').textContent,TEXT.revokeWarning);
 context.fox={...fox,name:'<script>alert(1)</script>'};
 vm.runInContext('drawItems([fox],"catalogItems",false);',context);
 assert.ok(!el('catalogItems').innerHTML.includes('<script>'));
 const moduleItem={type:1150,index:326009,id:'mod_regenerator_armor',name:'Armor Regeneration',name_source:'derived_id',editable:true};
 context.moduleItem=moduleItem;
 vm.runInContext('drawItems([moduleItem],"catalogItems",false);',context);
 assert.match(el('catalogItems').innerHTML,/Armor Regeneration/);
 assert.ok(el('catalogItems').innerHTML.includes(TEXT.derivedModuleName));
 assert.ok(el('catalogItems').innerHTML.includes('data-action="grant"'));
 assert.ok(!el('catalogItems').innerHTML.includes(TEXT.readOnly));
 vm.runInContext('openItem(moduleItem,"grant");',context);
 assert.equal(el('levelField').hidden,false);
 assert.equal(el('itemLevel').max,'10');
 assert.equal(el('itemLevel').value,1);
 assert.equal(el('itemWarning').textContent,TEXT.moduleGrantWarning);
 for(const [button,title,fields,rarity] of [
  ['grantAllWeapons','grantAllWeapons',false,false],
  ['grantAllWear','grantAllWear',true,true],
  ['grantAllArmor','grantAllArmor',false,true],
  ['grantAllGadgets','grantAllGadgets',false,true],
  ['grantAllModules','grantAllModules',false,true],
 ]) {
  el(button).onclick();
  assert.equal(el('bulkTitle').textContent,TEXT[title]);
  assert.equal(el('bulkFields').hidden,fields);
  assert.equal(el('bulkRarityField').hidden,rarity);
 }
 el('grantAllModules').onclick();
 assert.equal(el('bulkTitle').textContent,TEXT.grantAllModules);
 assert.equal(el('bulkDescription').textContent,TEXT.bulkGrantModulesHint);
 assert.equal(el('bulkLevel').max,'10');
 assert.equal(el('bulkLevel').value,10);
 await el('bulkPreview').onclick();
 assert.deepEqual(posted && JSON.parse(JSON.stringify(posted)), {action:'grant_modules',level:10,rarity:0,revision:4,updated_at:'timestamp'});
 el('grantAllPets').onclick();
 assert.equal(el('bulkTitle').textContent,TEXT.grantAllPets);
 assert.equal(el('bulkDescription').textContent,TEXT.bulkGrantPetsHint);
 assert.equal(el('bulkFields').hidden,false);
 assert.equal(el('bulkLevel').max,'65');
 el('bulkLevel').value=37;
 await el('bulkPreview').onclick();
 assert.deepEqual(posted && JSON.parse(JSON.stringify(posted)), {action:'grant_pets',level:37,rarity:0,revision:4,updated_at:'timestamp'});
 el('bulkConfirm').value='test-player';
 el('bulkConfirm').oninput();
 assert.equal(el('bulkApply').disabled,false);
 el('bulkForm').onsubmit({preventDefault(){}});
 await new Promise(resolve=>setImmediate(resolve));
 assert.equal(posted.confirm_player_id,'test-player');
 assert.equal(posted.plan_token,'preview-token');
 console.log('PASS: pet level grant/update, module editing, readable module labels, module level limits, module and pet bulk-grant forms and confirmation, equipped protection and escaping');
})().catch(e=>{console.error(e);process.exitCode=1;});
