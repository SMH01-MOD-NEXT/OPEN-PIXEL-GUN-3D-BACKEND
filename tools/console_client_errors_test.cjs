// Exercise the real renderers; reports must remain text, never executable markup.
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const root=require('node:path').resolve(__dirname,'..');
const source=fs.readFileSync(root+'/internal/api/admin_assets/console.js','utf8');
const html=fs.readFileSync(root+'/internal/api/admin_assets/console.html','utf8');
const nodes=new Map();const el=id=>{if(!nodes.has(id))nodes.set(id,{value:'',textContent:'',innerHTML:'',disabled:false,addEventListener(){}});return nodes.get(id)};
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let response,path;
const ctx=vm.createContext({el,esc,t:(key,v={})=>key+JSON.stringify(v),route:'clientErrors',language:'en',URLSearchParams,Date,report:e=>{throw e},api:async(method,url)=>{path=url;return response}});
const start=source.indexOf('  let diagnosticOffset =');const end=source.indexOf('  async function loadPlayers()',start);
vm.runInContext(source.slice(start,end),ctx);
for(const lang of ['ru','en']){
 const locale=JSON.parse(fs.readFileSync(root+`/internal/api/admin_assets/console.${lang}.json`));
 for(const key of ['clientErrors','clientErrorsHint','diagnostics24h','diagnosticErrors','diagnosticGroups','diagnosticRetention','diagnosticEmpty','diagnosticUnavailable']) assert.ok(locale[key]);
}
assert.match(html,/data-route="clientErrors"/);assert.match(html,/id="page-clientErrors"/);
(async()=>{
 vm.runInContext('renderDiagnosticStats({errors:3,groups:2,players:1,resyncs:1,completed:1})',ctx);
 assert.match(el('diagnosticStats').innerHTML,/>3</);assert.match(el('diagnosticStats').innerHTML,/diagnosticResyncs/);
 el('diagnosticQ').value='hello & world';el('diagnosticPlayer').value='player';
 response={groups:[{code:'<script>x</script>',source:'client',level:'error',message:'<img src=x onerror=evil()>',stack:'</pre><script>evil()</script>',player_id:'<b>evil</b>',count:4,players:2,first:'2026-10-23T06:00:00Z',last:'2026-10-23T06:01:00Z'}],total:51,summary:{}};
 await vm.runInContext('loadDiagnostics()',ctx);
 assert.ok(path.includes('q=hello+%26+world') && path.includes('player=player'));
 assert.ok(!el('diagnosticRows').innerHTML.includes('<script>'));
 assert.match(el('diagnosticRows').innerHTML,/&lt;script&gt;/);assert.equal(el('diagnosticPrev').disabled,true);assert.equal(el('diagnosticNext').disabled,false);
 response={groups:[],total:0,summary:{}};await vm.runInContext('loadDiagnostics()',ctx);assert.match(el('diagnosticResult').textContent,/diagnosticEmpty/);
 response={groups:[],total:0,summary:{unavailable:true}};await vm.runInContext('loadDiagnostics()',ctx);assert.match(el('diagnosticResult').textContent,/diagnosticUnavailable/);
 ctx.route='players';el('diagnosticRows').innerHTML='unchanged';await vm.runInContext('loadDiagnostics()',ctx);assert.equal(el('diagnosticRows').innerHTML,'unchanged');
 console.log('PASS: diagnostics filters, counters, paging, empty/unavailable states, RU/EN and XSS escaping');
})();
