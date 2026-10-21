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
 showPlayer:async()=>{}, api:async(method,url,body)=>{posted=body;return {changed:true};},
 category:item=>TEXT.types[String(item.type)],
 t:(key,values={})=>Object.entries(values).reduce((s,[k,v])=>s.split('{'+k+'}').join(String(v)),TEXT[key]||key),
 esc:value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]))});
const slice=(start,end)=>source.slice(source.indexOf(start),source.indexOf(end,source.indexOf(start)));
vm.runInContext('let busy=false, selected="test-player", snapshot={revision:4,updated_at:"timestamp",items:[]}, player={nick:"Test"}, itemContext=null;'+
 slice('  function drawItems(', '  function renderOwned(')+
 slice('  function openItem(', '  function invalidateBulk('),context);
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
for(const id of ['quantityField','levelField','rarityField']) assert.equal(el(id).hidden,true);
assert.equal(el('itemWarning').textContent,TEXT.petGrantWarning);
assert.equal(el('itemDialog').open,true);
(async()=>{
 el('itemForm').onsubmit({preventDefault(){}});
 await new Promise(resolve=>setImmediate(resolve));
 assert.deepEqual(posted && JSON.parse(JSON.stringify(posted)), {type:80,index:20003,action:'grant',quantity:1,level:1,rarity:0,revision:4,updated_at:'timestamp'});
 context.fox={...fox,quantity:1,level:27,equipped:true};
 vm.runInContext('drawItems([fox],"ownedItems",true);openItem(fox,"grant");',context);
 assert.match(el('ownedItems').innerHTML,/data-action="revoke" disabled/);
 assert.equal(el('itemLevel').value,1);
 vm.runInContext('openItem(fox,"revoke");',context);
 assert.equal(el('itemWarning').textContent,TEXT.revokeWarning);
 context.fox={...fox,name:'<script>alert(1)</script>'};
 vm.runInContext('drawItems([fox],"catalogItems",false);',context);
 assert.ok(!el('catalogItems').innerHTML.includes('<script>'));
 console.log('PASS: pet buttons, identity, derived label, baseline form/payload, existing-pet defaults, equipped protection, revoke guidance and escaping');
})().catch(e=>{console.error(e);process.exitCode=1;});
