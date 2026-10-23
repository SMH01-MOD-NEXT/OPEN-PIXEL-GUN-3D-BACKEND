// Exercise the actual news renderer/publisher without a browser dependency.
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm'),path=require('node:path');
const root=path.resolve(__dirname,'..'),source=fs.readFileSync(root+'/internal/api/admin_assets/console.js','utf8'),html=fs.readFileSync(root+'/internal/api/admin_assets/console.html','utf8');
const nodes=new Map();const el=id=>{if(!nodes.has(id))nodes.set(id,{value:'',textContent:'',innerHTML:'',disabled:false,reset(){this.resets=(this.resets||0)+1}});return nodes.get(id)};
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let response={news:[],limit:500},calls=[],notice='',error,release;
const ctx=vm.createContext({window:{confirm:()=>true},el,esc,t:(k,v={})=>k+JSON.stringify(v),route:'mailbox',language:'en',Date,status:(s)=>{notice=s},report:e=>{error=e},api:async(method,url,body)=>{calls.push({method,url,body});if(method==='POST'&&release)await new Promise(resolve=>{release.resolve=resolve});return response}});
vm.runInContext(source.slice(source.indexOf('  let mailboxSeq ='),source.indexOf('  async function loadStats()')),ctx);
for(const lang of ['ru','en']){
 const locale=JSON.parse(fs.readFileSync(root+`/internal/api/admin_assets/console.${lang}.json`));
 for(const k of ['mailbox','mailboxHint','mailboxRequired','mailboxPublished','mailboxPublish','mailboxCount','mailboxUpdate','mailboxDeveloperMessage','mailboxUpcoming','mailboxContest','mailboxEntertainment'])assert.ok(locale[k],lang+':'+k);
 for(const m of html.matchAll(/data-i18n="(mailbox[^"]+)"/g))assert.ok(locale[m[1]],lang+':'+m[1]);
}
assert.match(html,/data-route="mailbox"/);assert.match(html,/id="page-mailbox"/);
(async()=>{
 await vm.runInContext('loadMailboxNews()',ctx);assert.match(el('mailboxResult').textContent,/mailboxCount/);
 const text={English:'<script>evil()</script>',Russian:'Русский'};response={news:[{id:1,type:'Update',header:text,short_description:text,description:text,date:1792750000}],limit:500};
 await vm.runInContext('loadMailboxNews()',ctx);assert.ok(!el('mailboxNews').innerHTML.includes('<script>'));assert.match(el('mailboxNews').innerHTML,/&lt;script&gt;/);
 ctx.language='ru';await vm.runInContext('loadMailboxNews()',ctx);assert.match(el('mailboxNews').innerHTML,/Русский/);
 ctx.route='players';el('mailboxNews').innerHTML='preserved';await vm.runInContext('loadMailboxNews()',ctx);assert.equal(el('mailboxNews').innerHTML,'preserved');ctx.route='mailbox';
 let prevented=0;await el('mailboxForm').onsubmit({preventDefault(){prevented++}});assert.equal(prevented,1);assert.match(notice,/mailboxRequired/);assert.equal(calls.filter(c=>c.method==='POST').length,0);
 for(const name of ['Header','Summary','Body'])el('mailbox'+name+'Ru').value=' Тест ';
 el('mailboxType').value='Update';release={};const pending=el('mailboxForm').onsubmit({preventDefault(){}});assert.equal(el('mailboxPublish').disabled,true);
 await el('mailboxForm').onsubmit({preventDefault(){}});assert.equal(calls.filter(c=>c.method==='POST').length,1);release.resolve();await pending;
 const sent=calls.find(c=>c.method==='POST');assert.equal(sent.url,'/api/v1/admin/mailbox/news');assert.equal(sent.body.header.Russian,'Тест');assert.equal(sent.body.type,'Update');assert.equal(el('mailboxPublish').disabled,false);assert.equal(el('mailboxForm').resets,1);assert.match(notice,/mailboxPublished/);assert.equal(error,undefined);
 release=undefined;el('mailboxCoins').value='2626';el('mailboxGems').value='1010';el('mailboxWeapon').value='143001';el('mailboxPublishAt').value='2026-10-31T12:00:00';
 await el('mailboxForm').onsubmit({preventDefault(){}});
 const gift=calls.filter(c=>c.method==='POST').at(-1).body;assert.equal(gift.publish_at,'2026-10-31T12:00:00.000Z');assert.equal(gift.type,'DeveloperMessage');assert.equal(gift.rewards.coins,2626);assert.equal(gift.rewards.gems,1010);assert.equal(gift.rewards.weapon,143001);
 const prior=calls.filter(c=>c.method==='POST').length;el('mailboxCoins').value='-1';await el('mailboxForm').onsubmit({preventDefault(){}});assert.equal(calls.filter(c=>c.method==='POST').length,prior);
 const archiveButton={disabled:false,dataset:{mailboxArchive:'1'}};await el('mailboxNews').onclick({target:{closest:()=>archiveButton}});assert.equal(calls.filter(c=>c.method==='DELETE').at(-1).url,'/api/v1/admin/mailbox/news/1');
 console.log('PASS: mailbox RU/EN, XSS, list/empty/stale navigation, validation, publish payload, double-submit guard, UTC schedule, gifts and archive');
})().catch(e=>{console.error(e);process.exitCode=1});
