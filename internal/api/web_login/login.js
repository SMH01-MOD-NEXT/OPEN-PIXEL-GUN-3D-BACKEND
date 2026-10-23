'use strict';
(async () => {
 const texts = await (await fetch('/account/text.json', {cache: 'no-store'})).json();
 const language = document.querySelector('#language');
 language.value = navigator.language.startsWith('ru') ? 'ru' : 'en';
 let csrf = '';
 const status = document.querySelector('#status');
 const request = document.querySelector('#request-form');
 const verify = document.querySelector('#verify-form');
 const logout = document.querySelector('#logout');
 const t = () => texts[language.value];
 const translate = () => {document.documentElement.lang = language.value; document.title = t().title; language.setAttribute("aria-label", t().language); document.querySelectorAll('[data-text]').forEach(e => {e.textContent = t()[e.dataset.text];});};
 language.addEventListener('change', translate); translate();
 async function api(path, body, token) {
  const response = await fetch('/api/v1/web-auth/' + path, {method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', cache: 'no-store', headers: {'Content-Type': 'application/json', ...(token ? {'X-CSRF-Token': token} : {})}, body: body === undefined ? undefined : JSON.stringify(body)});
  const result = await response.json(); if (!response.ok) throw new Error('request failed'); return result;
 }
 function signed(result) {csrf = result.csrf_token; request.hidden = true; verify.hidden = true; logout.hidden = false; status.textContent = t().signed + ' ' + result.player_id;}
 async function action(button, callback) {button.disabled = true; try {await callback();} catch {status.textContent = t().error;} finally {button.disabled = false;}}
 request.addEventListener('submit', e => {e.preventDefault();action(request.querySelector('button'), async () => {const result = await api('challenge', {player_id: request.elements.player_id.value.trim()}); verify.hidden = false; status.textContent = t().pending + ' ' + result.reference; verify.elements.code.focus();});});
 verify.addEventListener('submit', e => {e.preventDefault();action(verify.querySelector('button'), async () => {const result = await api('verify', {code: verify.elements.code.value.trim()});verify.elements.code.value = '';signed(result);});});
 document.querySelector('#cancel').addEventListener('click', e => action(e.target, async () => {await api('cancel', {}); verify.hidden = true; verify.reset(); status.textContent = t().done;}));
 logout.addEventListener('click', () => action(logout, async () => {await api('logout', {}, csrf);csrf = ''; logout.hidden = true;request.hidden = false;status.textContent = t().done;}));
 try {signed(await api('me'));} catch { /* A first visit has no session. */ }
})();
