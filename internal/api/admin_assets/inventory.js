  var inventoryEpoch = 0;
  function inventoryText(key, values) {
    var text = INVENTORY_TEXT[key] || key;
    Object.keys(values || {}).forEach(function (k) { text = text.split('{' + k + '}').join(String(values[k])); });
    return text;
  }
  function mountInventory(id) {
    var host = el('inventoryPanel'), epoch = ++inventoryEpoch, snapshot = null, offset = 0, busy = false, searchSeq = 0;
    if (!host) { return; }
    function active() { return selected === id && inventoryEpoch === epoch && el('inventoryPanel') === host; }
    function category(item) { return (INVENTORY_TEXT.types[item.type] || item.category) + ' / ' + item.type; }
    function button(key, domID) { return '<button type="button" id="' + domID + '">' + esc(inventoryText(key)) + '</button>'; }
    function input(label, domID, min, max, value) {
      return '<label>' + esc(inventoryText(label)) + '<input type="number" id="' + domID + '" min="' + min + '" max="' + max + '" value="' + value + '"></label>';
    }
    host.innerHTML = '<h3>' + esc(inventoryText('title')) + '</h3><p class="hint">' + esc(inventoryText('hint')) + '</p>' +
      '<div class="row"><input id="invSearch" placeholder="' + esc(inventoryText('search')) + '"><select id="invType"><option value="0">' + esc(inventoryText('allTypes')) + '</option>' +
      Object.keys(INVENTORY_TEXT.types).map(function (t) { return '<option value="' + t + '">' + esc(INVENTORY_TEXT.types[t]) + '</option>'; }).join('') + '</select>' + button('searchButton', 'invFind') + '</div>' +
      '<div class="row">' + input('quantity', 'invQuantity', 1, 1000000, 1) + input('level', 'invLevel', 1, 65, 65) + input('rarity', 'invRarity', 0, 5, 0) + '</div>' +
      '<p id="invStatus" role="status"></p><h4>' + esc(inventoryText('owned')) + '</h4>' + button('reload', 'invReload') + '<div id="invOwned" style="max-height:350px;overflow:auto"></div>' +
      '<h4>' + esc(inventoryText('catalog')) + '</h4><div id="invCatalog" style="max-height:450px;overflow:auto"></div><p id="invCount" class="hint"></p>' +
      button('previous', 'invPrev') + button('next', 'invNext');
    function status(text) { if (active()) { el('invStatus').textContent = text; } }
    function draw(items, target, owned) {
      var box = el(target);
      box.innerHTML = '<table><thead><tr><th>' + esc(inventoryText('name')) + '</th><th>' + esc(inventoryText('category')) + '</th><th>' + esc(inventoryText('identifier')) + '</th><th></th></tr></thead><tbody>' + items.map(function (item, ix) {
        var detail = owned ? ' × ' + item.quantity + (item.level ? ' · L' + item.level : '') + (typeof item.rarity === 'number' ? ' · R' + item.rarity : '') : '';
        return '<tr><td>' + esc(item.name) + esc(detail) + (item.name_source === 'technical_id' ? '<br><span class="hint">' + esc(inventoryText('technical')) + '</span>' : '') + '</td><td>' + esc(category(item)) + '</td><td>' + esc(item.index) + '<br>' + esc(item.id) + '</td><td>' +
          (item.editable ? '<button data-item="' + ix + '" data-action="grant">' + esc(inventoryText('grant')) + '</button>' + (owned ? '<button data-item="' + ix + '" data-action="revoke"' + (item.equipped ? ' disabled' : '') + '>' + esc(inventoryText('revoke')) + '</button>' : '') + (item.equipped ? '<br>' + esc(inventoryText('equipped')) : '') : esc(inventoryText('readOnly'))) + '</td></tr>';
      }).join('') + '</tbody></table>' + (!items.length ? '<p class="hint">' + esc(inventoryText('empty')) + '</p>' : '');
      Array.prototype.forEach.call(box.querySelectorAll('button[data-item]'), function (b) {
        b.onclick = function () { edit(items[Number(b.getAttribute('data-item'))], b.getAttribute('data-action')); };
      });
    }
    function reload() {
      status(inventoryText('loading'));
      return api('GET', '/api/v1/admin/players/' + encodeURIComponent(id) + '/inventory').then(function (data) {
        if (!active()) { return; } snapshot = data; draw(data.items || [], 'invOwned', true); status('');
      }).catch(function (err) { status(err.message); });
    }
    function search() {
      var seq = ++searchSeq, query = el('invSearch').value.trim(), typ = el('invType').value;
      return api('GET', '/api/v1/admin/catalog?q=' + encodeURIComponent(query) + '&type=' + typ + '&offset=' + offset + '&limit=100').then(function (data) {
        if (!active() || seq !== searchSeq) { return; } draw(data.items || [], 'invCatalog', false);
        el('invCount').textContent = inventoryText('shown', {shown: (data.items || []).length, total: data.total});
        el('invPrev').disabled = offset === 0; el('invNext').disabled = offset + 100 >= data.total;
      }).catch(function (err) { status(err.message); });
    }
    function edit(item, action) {
      if (!active() || !snapshot || busy) { return; }
      var quantity = Number(el('invQuantity').value), level = Number(el('invLevel').value), rarity = Number(el('invRarity').value);
      if (!Number.isInteger(quantity) || quantity < 1 || quantity > 1000000 || !Number.isInteger(level) || level < 1 || level > 65 || !Number.isInteger(rarity) || rarity < 0 || rarity > 5) { status(inventoryText('invalid')); return; }
      if (!confirm(inventoryText('confirm', {player: id, action: inventoryText(action), name: item.name, type: item.type, id: item.index, quantity: quantity, level: level, rarity: rarity}))) { return; }
      busy = true;
      api('POST', '/api/v1/admin/players/' + encodeURIComponent(id) + '/inventory', {type: item.type, index: item.index, action: action, quantity: quantity, level: level, rarity: rarity, revision: snapshot.revision, updated_at: snapshot.updated_at}).then(function (data) {
        if (!active()) { return; }
        // Refresh the whole profile too: its raw editors must never retain a
        // pre-grant snapshot that the next operator save would restore.
        return showPlayer(id).then(function () { setStatus(inventoryText(data.changed ? 'saved' : 'unchanged')); });
      }).catch(function (err) { status(inventoryText('conflict') + ' ' + err.message); }).finally(function () { busy = false; });
    }
    el('invFind').onclick = function () { offset = 0; search(); };
    el('invSearch').onkeydown = function (event) { if (event.key === 'Enter') { offset = 0; search(); } };
    el('invReload').onclick = reload;
    el('invPrev').onclick = function () { offset = Math.max(0, offset - 100); search(); };
    el('invNext').onclick = function () { offset += 100; search(); };
    reload(); search();
  }
