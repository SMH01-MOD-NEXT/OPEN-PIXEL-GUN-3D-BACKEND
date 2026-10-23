// Exercise the actual edit/save/cancel implementation and localized errors.
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const root=path.resolve(__dirname,'..'),source=fs.readFileSync(root+'/internal/api/admin_assets/console.js','utf8');
const nodes=new Map();
const el=id=>{if(!nodes.has(id))nodes.set(id,{value:'',dataset:{},textContent:'',innerHTML:'',disabled:false,hidden:false,scrollIntoView(){this.scrolled=true},reset(){for(const [key,n]of nodes){if(/mailbox(Header|Summary|Body)/.test(key))n.value='';if(['mailboxCoins','mailboxGems','mailboxWeapon'].includes(key))n.value='0';if(key==='mailboxPublishAt')n.value='';if(key==='mailboxType')n.value='Update'}}});return nodes.get(id)};
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const en=JSON.parse(fs.readFileSync(root+'/internal/api/admin_assets/console.en.json')),ru=JSON.parse(fs.readFileSync(root+'/internal/api/admin_assets/console.ru.json'));
for(const k of ['mailboxEdit','mailboxSave','mailboxCancel','mailboxEditing','mailboxSaved','mailboxEditScheduled','mailboxEditLocked','mailboxConflict','mailboxEditArchived','mailboxEditMissing','mailboxDiscardConfirm']) {assert.ok(en[k],k);assert.ok(ru[k],k)}
const text={Russian:'Заголовок',English:'<img src=x onerror=evil()>'};
let rows=[{id:7,type:'DeveloperMessage',header:text,short_description:text,description:text,date:1793188800,ctime_change:1792750000,publish_at:'2026-10-28T12:00:00Z',rewards:{coins:2626,gems:1010,weapon:143001},delivery_editable:true}],calls=[],notice='',reported,confirm=true,fail,release;
const ctx=vm.createContext({window:{confirm:()=>confirm},el,esc,t:(k,v={})=>k+JSON.stringify(v),route:'mailbox',language:'en',Date,status:s=>{notice=s},report:e=>{reported=e},api:async(method,url,body)=>{calls.push({method,url,body});if(method==='PUT'){if(release)await new Promise(resolve=>{release.resolve=resolve});if(fail)throw {code:fail};return {news:rows[0]}}return {news:rows,limit:500}}});
vm.runInContext(source.slice(source.indexOf('  let mailboxSeq ='),source.indexOf('  async function loadStats()')),ctx);
const clickEdit=id=>el('mailboxNews').onclick({target:{closest:()=>({dataset:{mailboxEdit:String(id)},disabled:false})}});
const submit=()=>el('mailboxForm').onsubmit({preventDefault(){}});
(async()=>{
 await vm.runInContext('loadMailboxNews()',ctx);
 assert.match(el('mailboxNews').innerHTML,/data-mailbox-edit="7"/);assert.ok(!el('mailboxNews').innerHTML.includes('<img'));
 await clickEdit(7);
 assert.equal(el('mailboxHeaderRu').value,'Заголовок');assert.equal(el('mailboxBodyEn').value,text.English);
 assert.equal(el('mailboxPublishAt').value,'2026-10-28T12:00:00');assert.equal(el('mailboxCoins').value,2626);
 assert.equal(el('mailboxPublish').dataset.i18n,'mailboxSave');assert.equal(el('mailboxCancel').hidden,false);assert.equal(el('mailboxCoins').disabled,false);assert.equal(el('mailboxForm').scrolled,true);
 el('mailboxHeaderRu').value='Изменено';el('mailboxForm').oninput();release={};const pending=submit();assert.equal(el('mailboxPublish').disabled,true);assert.equal(el('mailboxCancel').disabled,true);
 await submit();await clickEdit(7);el('mailboxCancel').onclick();assert.equal(calls.filter(c=>c.method==='PUT').length,1);
 release.resolve();await pending;release=undefined;
 const sent=calls.find(c=>c.method==='PUT');assert.equal(sent.url,'/api/v1/admin/mailbox/news/7');assert.equal(sent.body.expected_ctime_change,1792750000);assert.equal(sent.body.header.Russian,'Изменено');assert.equal(sent.body.publish_at,'2026-10-28T12:00:00.000Z');assert.equal(sent.body.rewards.weapon,143001);assert.match(notice,/mailboxSaved/);assert.equal(el('mailboxPublish').dataset.i18n,'mailboxPublish');assert.equal(el('mailboxCancel').hidden,true);assert.equal(el('mailboxPublish').disabled,false);
 // Conflict preserves unsaved contents and the original precondition, never
 // upgrades the old form to a new revision and silently overwrites another edit.
 await clickEdit(7);el('mailboxBodyRu').value='Несохранённый текст';el('mailboxForm').oninput();fail='mailbox_conflict';rows=[{...rows[0],ctime_change:1792759999}];await submit();assert.match(notice,/mailboxConflict/);assert.equal(el('mailboxBodyRu').value,'Несохранённый текст');assert.equal(el('mailboxPublish').dataset.i18n,'mailboxSave');assert.equal(calls.filter(c=>c.method==='PUT').at(-1).body.expected_ctime_change,1792750000);
 confirm=false;await clickEdit(7);el('mailboxCancel').onclick();assert.equal(el('mailboxBodyRu').value,'Несохранённый текст');confirm=true;el('mailboxCancel').onclick();assert.equal(el('mailboxCancel').hidden,true);
 fail=undefined;rows=[{...rows[0],publish_at:undefined,delivery_editable:false}];await vm.runInContext('loadMailboxNews()',ctx);await clickEdit(7);
 for(const id of ['mailboxPublishAt','mailboxCoins','mailboxGems','mailboxWeapon','mailboxType'])assert.equal(el(id).disabled,true,id);
 assert.match(el('mailboxEditorStatus').textContent,/mailboxEditLocked/);assert.equal(el('mailboxHeaderRu').disabled,false);
 await submit();assert.equal(calls.filter(c=>c.method==='PUT').at(-1).body.expected_ctime_change,1792759999);
 assert.equal(el('mailboxCoins').disabled,false);assert.equal(el('mailboxType').disabled,false);
 rows=[{...rows[0],archived:true}];await vm.runInContext('loadMailboxNews()',ctx);assert.ok(!el('mailboxNews').innerHTML.includes('data-mailbox-edit'));await clickEdit(7);assert.equal(el('mailboxPublish').dataset.i18n,'mailboxPublish');assert.equal(reported,undefined);
 console.log('PASS: mailbox editing prefill, UTC, exact PUT/revision, RU/EN, XSS, double-submit, cancel/discard, conflict preservation, published locks and archived guards');
})().catch(e=>{console.error(e);process.exitCode=1});
