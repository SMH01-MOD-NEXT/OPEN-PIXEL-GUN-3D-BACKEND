// SPDX-License-Identifier: GPL-3.0-or-later

package api

// The operator console: one self-contained page served by this server.
//
// It has no external dependencies on purpose. This backend runs on a LAN with
// no route to the internet, so a page that pulled a framework from a CDN would
// be a blank screen exactly when it is needed.

import "net/http"

// handleAdminConsole serves the console page.
//
// The page itself is deliberately not behind the admin token: it has to load
// before an operator can type one. It ships no data of its own, only markup and
// script, and every request it makes carries the token in an Authorization
// header. The token lives in sessionStorage rather than localStorage so closing
// the tab ends the session, and it is never placed in a URL, where it would be
// captured by this server's own request log.
func (s *Server) handleAdminConsole(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	// Everything is inline and no external origin is contacted, so the policy
	// can be this tight.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(adminConsoleHTML))
}

const adminConsoleHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OPEN PG BACKEND</title>
<style>
:root{--bg:#0e1116;--panel:#161b22;--line:#272e38;--text:#e6edf3;--dim:#8b949e;--acc:#3d8bfd;--ok:#2ea043;--warn:#d29922;--bad:#da3633}
*{box-sizing:border-box}
body{margin:0;font:14px/1.5 -apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;background:var(--bg);color:var(--text)}
header{display:flex;gap:12px;align-items:center;flex-wrap:wrap;padding:12px 18px;background:var(--panel);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:5}
header h1{font-size:15px;margin:0;font-weight:600;letter-spacing:.02em}
.grow{flex:1}
input,select,button,textarea{font:inherit;color:var(--text);background:#0d1117;border:1px solid var(--line);border-radius:6px;padding:6px 9px}
input:focus,select:focus,textarea:focus{outline:1px solid var(--acc)}
button{cursor:pointer;background:#21262d;transition:background .12s}
button:hover{background:#2d333b}
button.p{background:var(--acc);border-color:var(--acc)}
button.d{background:var(--bad);border-color:var(--bad)}
button.g{background:var(--ok);border-color:var(--ok)}
button:disabled{opacity:.5;cursor:default}
main{padding:18px;display:grid;gap:18px;grid-template-columns:1fr}
@media(min-width:1200px){main{grid-template-columns:1.35fr 1fr}}
section{background:var(--panel);border:1px solid var(--line);border-radius:10px;overflow:hidden}
section>h2{margin:0;padding:11px 14px;font-size:13px;font-weight:600;border-bottom:1px solid var(--line);color:var(--dim);text-transform:uppercase;letter-spacing:.06em}
.body{padding:14px}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(120px,1fr));gap:10px}
.card{background:#0d1117;border:1px solid var(--line);border-radius:8px;padding:10px 12px}
.card b{display:block;font-size:21px;font-weight:600}
.card span{color:var(--dim);font-size:12px}
table{width:100%;border-collapse:collapse}
th,td{text-align:left;padding:7px 9px;border-bottom:1px solid var(--line);font-size:13px;white-space:nowrap}
th{color:var(--dim);font-weight:500;font-size:12px}
tbody tr{cursor:pointer}
tbody tr:hover{background:#1b2230}
tbody tr.sel{background:#1f2a3d}
.scroll{max-height:340px;overflow:auto}
.row{display:flex;gap:8px;align-items:center;flex-wrap:wrap;margin-bottom:9px}
.row label{color:var(--dim);font-size:12px;min-width:92px}
.row input{flex:1;min-width:90px}
.pill{display:inline-block;padding:1px 7px;border-radius:99px;font-size:11px;border:1px solid var(--line)}
.pill.bad{background:rgba(218,54,51,.18);border-color:var(--bad);color:#ff9d99}
.pill.ok{background:rgba(46,160,67,.16);border-color:var(--ok);color:#7ee2a0}
#log{font:12px/1.45 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;background:#0a0d12;height:420px;overflow:auto;padding:10px;white-space:pre-wrap;word-break:break-word}
#log div{padding:1px 0;border-bottom:1px solid rgba(255,255,255,.03)}
#log div.k{color:#7ee2a0}
#log div.w{color:#ffd479}
#log div.e{color:#ff9d99}
#status{color:var(--dim);font-size:12px}
.hint{color:var(--dim);font-size:12px;margin:0 0 10px}
h3{font-size:12px;color:var(--dim);text-transform:uppercase;letter-spacing:.05em;margin:16px 0 9px;padding-top:14px;border-top:1px solid var(--line)}
h3:first-child{margin-top:0;padding-top:0;border-top:0}
.raw-grid{display:grid;grid-template-columns:1fr;gap:10px}
.raw-grid label{display:block;color:var(--dim);font-size:12px}
textarea.json{display:block;width:100%;min-height:150px;margin-top:5px;resize:vertical;font:12px/1.4 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;tab-size:2;white-space:pre}
.meta{font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;word-break:break-all}
</style>
</head>
<body>
<header>
  <h1>OPEN PG BACKEND</h1>
  <span id="status">введите токен администратора</span>
  <span class="grow"></span>
  <input id="token" type="password" placeholder="admin token" style="width:190px">
  <button class="p" id="connect">Подключиться</button>
  <button id="refresh">Обновить</button>
</header>

<main>
  <div style="display:grid;gap:18px">
    <section>
      <h2>Статистика</h2>
      <div class="body"><div class="cards" id="stats"></div></div>
    </section>

    <section>
      <h2>Игроки</h2>
      <div class="body">
        <div class="row">
          <input id="q" placeholder="id, ник, устройство или адрес">
          <label style="min-width:auto"><input type="checkbox" id="onlyBanned" style="flex:none"> только забаненные</label>
          <button id="find">Найти</button>
        </div>
        <div class="scroll">
          <table>
            <thead><tr><th>ID</th><th>Ник</th><th>Ур.</th><th>Опыт</th><th>Монеты</th><th>Гемы</th><th>Статус</th><th>Заходил</th></tr></thead>
            <tbody id="players"></tbody>
          </table>
        </div>
        <p class="hint" id="playersHint" style="margin:10px 0 0"></p>
      </div>
    </section>

    <section>
      <h2>Выбранный игрок</h2>
      <div class="body" id="detail"><p class="hint">Выберите игрока в списке.</p></div>
    </section>
  </div>

  <div style="display:grid;gap:18px">
    <section>
      <h2>Операции поддержки</h2>
      <div class="body">
        <h3>Смена ID</h3>
        <p class="hint">Аккаунт, профиль и баны переносятся на новый id. Бан не снимается сменой id.</p>
        <div class="row"><label>Старый id</label><input id="cidOld"></div>
        <div class="row"><label>Новый id</label><input id="cidNew" placeholder="9 цифр"></div>
        <button class="p" id="doChangeId">Сменить id</button>

        <h3>Восстановление аккаунта</h3>
        <p class="hint">Прогресс переносится со старого id на новый. Оба входа продолжают работать, источник обнуляется.</p>
        <div class="row"><label>Откуда</label><input id="trFrom"></div>
        <div class="row"><label>Куда</label><input id="trTo"></div>
        <button class="p" id="doTransfer">Перенести прогресс</button>

        <h3>Объединение дублей</h3>
        <p class="hint">HTTP-аккаунт объединяется с игровым id, а старый id сохраняется как alias. Если прогресс есть у обоих, сервер откажет без потери данных.</p>
        <div class="row"><label>HTTP/source id</label><input id="rcSource"></div>
        <div class="row"><label>Игровой/target id</label><input id="rcTarget"></div>
        <button class="p" id="doReconcile">Объединить аккаунты</button>
      </div>
    </section>

    <section>
      <h2>Логи в реальном времени</h2>
      <div class="body" style="padding-bottom:0">
        <div class="row">
          <button class="g" id="logStart">Старт</button>
          <button id="logStop" disabled>Стоп</button>
          <input id="logFilter" placeholder="фильтр по подстроке">
          <button id="logClear">Очистить</button>
        </div>
      </div>
      <div id="log"></div>
    </section>
  </div>
</main>

<script>
(function () {
  var token = sessionStorage.getItem('opg3d_admin_token') || '';
  var selected = null;
  var stream = null;
  var lines = [];

  var el = function (id) { return document.getElementById(id); };
  el('token').value = token;

  function setStatus(text, bad) {
    var node = el('status');
    node.textContent = text;
    node.style.color = bad ? '#ff9d99' : '';
  }

  function headers() {
    return { 'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json' };
  }

  function api(method, path, body) {
    return fetch(path, {
      method: method,
      headers: headers(),
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (res) {
      return res.text().then(function (raw) {
        var data = {};
        if (raw) { try { data = JSON.parse(raw); } catch (e) { data = { detail: raw }; } }
        if (!res.ok) {
          throw new Error(data.detail || data.error || ('HTTP ' + res.status));
        }
        return data;
      });
    });
  }

  function esc(value) {
    if (value === null || value === undefined) { return ''; }
    return String(value).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function pretty(value) {
    return JSON.stringify(value || {}, null, 2);
  }

  function parseObject(id, label) {
    var value;
    try { value = JSON.parse(el(id).value); }
    catch (err) { throw new Error(label + ': ' + err.message); }
    if (!value || Array.isArray(value) || typeof value !== 'object') {
      throw new Error(label + ': ожидается JSON-объект');
    }
    return value;
  }

  function ago(iso) {
    if (!iso) { return '-'; }
    var delta = (Date.now() - new Date(iso).getTime()) / 1000;
    if (isNaN(delta)) { return '-'; }
    if (delta < 60) { return Math.max(0, Math.round(delta)) + ' с'; }
    if (delta < 3600) { return Math.round(delta / 60) + ' мин'; }
    if (delta < 86400) { return Math.round(delta / 3600) + ' ч'; }
    return Math.round(delta / 86400) + ' дн';
  }

  function card(label, value) {
    return '<div class="card"><b>' + esc(value) + '</b><span>' + esc(label) + '</span></div>';
  }

  function loadStats() {
    return api('GET', '/api/v1/admin/stats').then(function (data) {
      var s = data.stats || {};
      el('stats').innerHTML =
        card('аккаунтов', s.accounts) +
        card('профилей', s.profiles) +
        card('на связи сейчас', data.sessions) +
        card('за час', s.seen_last_hour) +
        card('за сутки', s.seen_today) +
        card('новых за сутки', s.new_today) +
        card('активных банов', s.active_bans) +
        card('версия', data.version || '-');
      setStatus('сервер ' + (data.server || '') + (data.maintenance ? ' - режим обслуживания' : ''));
    });
  }

  function loadPlayers() {
    var url = '/api/v1/admin/players?limit=300';
    var q = el('q').value.trim();
    if (q) { url += '&q=' + encodeURIComponent(q); }
    if (el('onlyBanned').checked) { url += '&banned=1'; }
    return api('GET', url).then(function (data) {
      var rows = data.players || [];
      el('players').innerHTML = rows.map(function (p) {
        var profile = p.profile || {};
        var status = p.banned
          ? '<span class="pill bad">бан' + (p.ban_reason ? ': ' + esc(p.ban_reason) : '') + '</span>'
          : '<span class="pill ok">ок</span>';
        return '<tr data-id="' + esc(p.player_id) + '">' +
          '<td>' + esc(p.player_id) + '</td>' +
          '<td>' + esc(p.nick) + '</td>' +
          '<td>' + esc(profile.level || 0) + '</td>' +
          '<td>' + esc(profile.experience || 0) + '</td>' +
          '<td>' + esc(profile.coins || 0) + '</td>' +
          '<td>' + esc(profile.gems || 0) + '</td>' +
          '<td>' + status + '</td>' +
          '<td>' + esc(ago(p.last_seen_at)) + '</td>' +
          '</tr>';
      }).join('');
      el('playersHint').textContent = 'показано ' + (data.shown || 0) + ' из ' + (data.total || 0);
      Array.prototype.forEach.call(el('players').querySelectorAll('tr'), function (tr) {
        tr.onclick = function () { showPlayer(tr.getAttribute('data-id')); };
        if (selected && tr.getAttribute('data-id') === selected) { tr.className = 'sel'; }
      });
    });
  }

  function showPlayer(id) {
    selected = id;
    Array.prototype.forEach.call(el('players').querySelectorAll('tr'), function (tr) {
      tr.className = tr.getAttribute('data-id') === id ? 'sel' : '';
    });
    el('cidOld').value = id;
    el('trFrom').value = id;
    el('rcTarget').value = id;

    return api('GET', '/api/v1/admin/players/' + encodeURIComponent(id)).then(function (data) {
      var p = data.player || {};
      var f = p.profile || {};
      var banLine = p.banned
        ? '<p class="hint">Забанен (' + esc(p.ban_kind) + ')' + (p.ban_reason ? ', причина: ' + esc(p.ban_reason) : '') + (p.ban_until ? ', до ' + esc(p.ban_until) : ', бессрочно') + '</p>'
        : '';
      var transferred = '';
      if (f.transferred_from) { transferred += '<p class="hint">Прогресс получен с ' + esc(f.transferred_from) + '</p>'; }
      if (f.transferred_to) { transferred += '<p class="hint">Прогресс отдан на ' + esc(f.transferred_to) + '</p>'; }
      var aliases = (p.aliases || []).length ? p.aliases.join(', ') : '-';

      el('detail').innerHTML =
        '<h3>' + esc(p.nick || '-') + ' &middot; ' + esc(p.player_id) + '</h3>' +
        banLine + transferred +
        '<p class="hint meta">aliases: ' + esc(aliases) + '<br>socket device: ' + esc(p.socket_device || '-') + '<br>HTTP device: ' + esc(p.device_id || '-') + '<br>IP: ' + esc(p.last_ip || '-') + '<br>created: ' + esc(p.created_at || '-') + '<br>last seen: ' + esc(p.last_seen_at || '-') + '<br>profile updated: ' + esc(f.updated_at || '-') + '</p>' +
        '<div class="row"><label>Ник</label><input id="pfNick" value="' + esc(f.nick || p.nick || '') + '"></div>' +
        '<div class="row"><label>Уровень</label><input id="pfLevel" type="number" min="0" value="' + esc(f.level || 0) + '"></div>' +
        '<div class="row"><label>Опыт</label><input id="pfExp" type="number" min="0" value="' + esc(f.experience || 0) + '"></div>' +
        '<div class="row"><label>Монеты</label><input id="pfCoins" type="number" min="0" value="' + esc(f.coins || 0) + '"></div>' +
        '<div class="row"><label>Гемы</label><input id="pfGems" type="number" min="0" value="' + esc(f.gems || 0) + '"></div>' +
        '<div class="row"><button class="p" id="pfSave">Сохранить счётчики</button></div>' +
        '<h3>Полное состояние</h3>' +
        '<p class="hint">Неизвестные серверу поля сохраняются. Каждый редактор должен содержать JSON-объект; пустой объект {} очищает соответствующий раздел.</p>' +
        '<div class="raw-grid">' +
          '<label>Prefs<textarea class="json" id="rawPrefs">' + esc(pretty(f.prefs)) + '</textarea></label>' +
          '<label>Progress<textarea class="json" id="rawProgress">' + esc(pretty(f.progress)) + '</textarea></label>' +
          '<label>Progress document<textarea class="json" id="rawDocument">' + esc(pretty(f.progress_document)) + '</textarea></label>' +
          '<label>Progress slots<textarea class="json" id="rawSlots">' + esc(pretty(f.progress_slots)) + '</textarea></label>' +
          '<label>Client state<textarea class="json" id="rawClientState">' + esc(pretty(f.client_state)) + '</textarea></label>' +
        '</div>' +
        '<div class="row" style="margin-top:10px"><button class="p" id="rawSave">Проверить и сохранить всё состояние</button></div>' +
        '<h3>Модерация</h3>' +
        '<div class="row"><label>Причина</label><input id="bnReason" placeholder="читы"></div>' +
        '<div class="row"><label>Минуты</label><input id="bnMinutes" type="number" min="0" value="0" placeholder="0 = бессрочно"></div>' +
        '<div class="row"><label style="min-width:auto"><input type="checkbox" id="bnDevice" checked style="flex:none"> также забанить устройство</label></div>' +
        '<div class="row"><button class="d" id="bnDo">Забанить</button><button class="g" id="bnUndo">Разбанить</button><span class="grow"></span><button class="d" id="pfDelete">Удалить аккаунт</button></div>';

      el('pfSave').onclick = function () {
        run(api('PUT', '/api/v1/admin/players/' + encodeURIComponent(id) + '/profile', {
          nick: el('pfNick').value.trim(),
          level: Number(el('pfLevel').value) || 0,
          experience: Number(el('pfExp').value) || 0,
          coins: Number(el('pfCoins').value) || 0,
          gems: Number(el('pfGems').value) || 0
        }), 'профиль сохранён', id);
      };
      el('rawSave').onclick = function () {
        var state;
        try {
          state = {
            prefs: parseObject('rawPrefs', 'Prefs'),
            progress: parseObject('rawProgress', 'Progress'),
            progress_document: parseObject('rawDocument', 'Progress document'),
            progress_slots: parseObject('rawSlots', 'Progress slots'),
            client_state: parseObject('rawClientState', 'Client state')
          };
        } catch (err) {
          setStatus(err.message, true);
          return;
        }
        if (!confirm('Сохранить полное состояние ' + id + '? Значения в редакторах заменят текущие разделы.')) { return; }
        run(api('PUT', '/api/v1/admin/players/' + encodeURIComponent(id) + '/state', state), 'полное состояние сохранено', id);
      };
      el('bnDo').onclick = function () {
        run(api('POST', '/api/v1/admin/players/' + encodeURIComponent(id) + '/ban', {
          reason: el('bnReason').value.trim(),
          minutes: Number(el('bnMinutes').value) || 0,
          include_device: el('bnDevice').checked
        }), 'забанен', id);
      };
      el('bnUndo').onclick = function () {
        run(api('POST', '/api/v1/admin/players/' + encodeURIComponent(id) + '/unban', {}), 'разбанен', id);
      };
      el('pfDelete').onclick = function () {
        if (!confirm('Удалить аккаунт ' + id + '? Баны сохранятся.')) { return; }
        selected = null;
        el('detail').innerHTML = '<p class="hint">Выберите игрока в списке.</p>';
        run(api('DELETE', '/api/v1/admin/players/' + encodeURIComponent(id)), 'аккаунт удалён');
      };
    });
  }

  function run(promise, okText, reselect) {
    return promise.then(function () {
      setStatus(okText);
      return Promise.all([loadStats(), loadPlayers()]);
    }).then(function () {
      if (reselect) { return showPlayer(reselect); }
    }).catch(function (err) { setStatus(err.message, true); });
  }

  function appendLog(entry) {
    lines.push(entry);
    if (lines.length > 2000) { lines = lines.slice(-1500); }
    renderLog(entry);
  }

  function classOf(entry) {
    var kind = entry.kind || '';
    if (kind.indexOf('unanswered') >= 0 || kind.indexOf('unreadable') >= 0 || kind.indexOf('undelivered') >= 0 || kind.indexOf('unbound') >= 0) { return 'w'; }
    if (kind.indexOf('banned') >= 0 || kind.indexOf('denied') >= 0 || kind.indexOf('failed') >= 0) { return 'e'; }
    if (kind.indexOf('answered') >= 0 || kind.indexOf('bound') >= 0) { return 'k'; }
    return '';
  }

  function renderLog(entry) {
    var filter = el('logFilter').value.trim().toLowerCase();
    if (filter && entry.line.toLowerCase().indexOf(filter) < 0) { return; }
    var pane = el('log');
    var atBottom = pane.scrollTop + pane.clientHeight >= pane.scrollHeight - 40;
    var div = document.createElement('div');
    div.className = classOf(entry);
    div.textContent = entry.line;
    pane.appendChild(div);
    while (pane.childNodes.length > 1200) { pane.removeChild(pane.firstChild); }
    if (atBottom) { pane.scrollTop = pane.scrollHeight; }
  }

  function redrawLog() {
    el('log').innerHTML = '';
    lines.forEach(renderLog);
  }

  function startLogs() {
    stopLogs();
    var controller = new AbortController();
    stream = controller;
    el('logStart').disabled = true;
    el('logStop').disabled = false;

    fetch('/api/v1/admin/logs/stream', { headers: headers(), signal: controller.signal }).then(function (res) {
      if (!res.ok) { throw new Error('поток отклонён: HTTP ' + res.status); }
      var reader = res.body.getReader();
      var decoder = new TextDecoder();
      var buffer = '';
      function pump() {
        return reader.read().then(function (chunk) {
          if (chunk.done) { return; }
          buffer += decoder.decode(chunk.value, { stream: true });
          var parts = buffer.split('\n\n');
          buffer = parts.pop();
          parts.forEach(function (block) {
            block.split('\n').forEach(function (row) {
              if (row.indexOf('data: ') !== 0) { return; }
              try { appendLog(JSON.parse(row.slice(6))); } catch (e) { /* a partial frame */ }
            });
          });
          return pump();
        });
      }
      return pump();
    }).catch(function (err) {
      if (err.name !== 'AbortError') { setStatus('логи: ' + err.message, true); }
    }).then(function () {
      el('logStart').disabled = false;
      el('logStop').disabled = true;
    });
  }

  function stopLogs() {
    if (stream) { stream.abort(); stream = null; }
    el('logStart').disabled = false;
    el('logStop').disabled = true;
  }

  function connect() {
    token = el('token').value.trim();
    sessionStorage.setItem('opg3d_admin_token', token);
    Promise.all([loadStats(), loadPlayers()])
      .then(function () { startLogs(); })
      .catch(function (err) { setStatus(err.message, true); });
  }

  el('connect').onclick = connect;
  el('refresh').onclick = function () {
    Promise.all([loadStats(), loadPlayers()]).catch(function (err) { setStatus(err.message, true); });
  };
  el('find').onclick = loadPlayers;
  el('q').onkeydown = function (e) { if (e.key === 'Enter') { loadPlayers(); } };
  el('onlyBanned').onchange = loadPlayers;
  el('logStart').onclick = startLogs;
  el('logStop').onclick = stopLogs;
  el('logClear').onclick = function () { lines = []; el('log').innerHTML = ''; };
  el('logFilter').oninput = redrawLog;

  el('doChangeId').onclick = function () {
    run(api('POST', '/api/v1/admin/change-id', {
      old_id: el('cidOld').value.trim(),
      new_id: el('cidNew').value.trim()
    }), 'id изменён');
  };
  el('doTransfer').onclick = function () {
    var from = el('trFrom').value.trim();
    var to = el('trTo').value.trim();
    if (!confirm('Перенести прогресс ' + from + ' на ' + to + '? Источник будет обнулён.')) { return; }
    run(api('POST', '/api/v1/admin/transfer', { from_id: from, to_id: to }), 'прогресс перенесён', to);
  };
  el('doReconcile').onclick = function () {
    var source = el('rcSource').value.trim();
    var target = el('rcTarget').value.trim();
    if (!confirm('Объединить HTTP-аккаунт ' + source + ' с игровым id ' + target + '?')) { return; }
    run(api('POST', '/api/v1/admin/reconcile', { source_id: source, target_id: target }), 'аккаунты объединены', target);
  };

  if (token) { connect(); }
})();
</script>
</body>
</html>
`
