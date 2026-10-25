// SPDX-License-Identifier: GPL-3.0-or-later
// Dependency-free interaction tests; run with TZ=Europe/Volgograd as well as UTC.
const assert = require('node:assert/strict'), fs = require('node:fs'), vm = require('node:vm');
const source = fs.readFileSync('internal/api/admin_assets/console.js', 'utf8');
const html = fs.readFileSync('internal/api/admin_assets/console.html', 'utf8');
const nodes = new Map();
const el = id => {
  if (!nodes.has(id)) nodes.set(id, {value:'', textContent:'', checked:false, reportValidity:()=>true});
  return nodes.get(id);
};
let calls = [], confirms = [], allowConfirm = true, inFlight = null, refreshes = [], reject = false, dirty = false;
let state = {maintenance:false, maintenance_manual:false, maintenance_schedule:{start:'2026-10-26T12:00:00Z',end:'2026-11-02T12:00:00Z'}};
const context = vm.createContext({
  el, Date, Number, selected:'123456789', base:id=>'/api/v1/admin/players/'+id,
  discard:()=>{if(dirty)el('maintenanceAllowedFlag').checked=false;return true;},
  confirm:key=>{confirms.push(key);return allowConfirm;}, t:key=>key,
  showPlayer:async id=>refreshes.push(id),
  api:async(method,url,body)=>{
    calls.push({method,url,body});
    if (reject) throw new Error('save failed');
    if (method==='GET') return structuredClone(state);
    if (method==='PATCH') {
      state = {...state, ...body, maintenance_manual:body.maintenance};
    }
    return structuredClone(state);
  },
  mutate:(button,errorID,fn)=>{
    if(inFlight)return;
    button.disabled=true;
    inFlight=Promise.resolve().then(fn).catch(e=>{el(errorID).textContent=e.message;}).finally(()=>{button.disabled=false;inFlight=null;});
    return inFlight;
  },
});
const start=source.indexOf('  let maintenanceView = null;'), end=source.indexOf('  async function navigate',start);
assert.ok(start>0 && end>start);
vm.runInContext(source.slice(start,end),context);
const click=async id=>{el(id).onclick();if(inFlight)await inFlight;};
const submit=async()=>{el('maintenanceForm').onsubmit({preventDefault(){}});if(inFlight)await inFlight;};
(async()=>{
  await vm.runInContext('loadMaintenance()',context);
  assert.equal(el('maintenanceStart').value,'2026-10-26T12:00:00');
  assert.equal(el('maintenanceEnd').value,'2026-11-02T12:00:00');
  el('maintenanceStart').value='2026-10-26T12:00:00';el('maintenanceEnd').value='2026-11-02T12:00:00';
  await submit();
  const scheduled=calls.find(c=>c.method==='PATCH');
  assert.equal(scheduled.body.maintenance,false);
  assert.equal(scheduled.body.maintenance_schedule.start,'2026-10-26T12:00:00.000Z');
  assert.equal(scheduled.body.maintenance_schedule.end,'2026-11-02T12:00:00.000Z');
  let writes=()=>calls.filter(c=>c.method!=='GET').length;
  const before=writes();el('maintenanceEnd').value='2026-10-25T12:00:00';await submit();assert.equal(writes(),before);assert.equal(el('maintenanceResult').textContent,'maintenanceInvalid');
  state.maintenance_schedule={start:new Date(Date.now()+3600000).toISOString(),end:new Date(Date.now()+7200000).toISOString()};
  await vm.runInContext('loadMaintenance()',context);
  allowConfirm=false;await click('maintenanceNow');assert.equal(writes(),before);allowConfirm=true;
  const plannedEnd=state.maintenance_schedule.end;
  await click('maintenanceNow');assert.equal(state.maintenance,false);assert.equal(state.maintenance_schedule.end,plannedEnd);assert.ok(new Date(state.maintenance_schedule.start)<=new Date());
  await click('maintenanceStop');assert.equal(state.maintenance,false);assert.equal(state.maintenance_schedule.start,'');assert.equal(state.maintenance_schedule.end,'');
  await click('maintenanceNow');assert.equal(state.maintenance,true);assert.equal(state.maintenance_schedule.end,'');
  dirty=true;el('maintenanceAllowedFlag').checked=true;
  await click('saveMaintenanceAccess');assert.ok(calls.some(c=>c.method==='PUT'&&c.url==='/api/v1/admin/players/123456789/maintenance-access'&&c.body.enabled===true));assert.deepEqual(refreshes,['123456789']);
  reject=true;await click('maintenanceStop');assert.equal(el('maintenanceResult').textContent,'save failed');assert.equal(el('maintenanceStop').disabled,false);
  assert.ok(confirms.includes('maintenanceNowConfirm')&&confirms.includes('maintenanceStopConfirm'));
  for(const lang of ['ru','en']) {
    const locale=JSON.parse(fs.readFileSync('internal/api/admin_assets/console.'+lang+'.json','utf8'));
    for(const key of source.slice(start,end).matchAll(/t\("(maintenance\w+)"\)/g))assert.ok(locale[key[1]],lang+': '+key[1]);
    for(const key of html.matchAll(/data-i18n="(maintenance\w*)"/g))assert.ok(locale[key[1]],lang+': '+key[1]);
  }
  assert.match(source,/player\.maintenance_allowed/);
  assert.match(html,/data-route="maintenance"/);
  assert.match(source,/if \(route === "maintenance"\) await loadMaintenance/);
  console.log('PASS: UTC schedule, validation, early start/end retention, confirmations, manual start/stop, dirty-player bypass, error recovery and RU/EN');
})().catch(e=>{console.error(e);process.exitCode=1;});
