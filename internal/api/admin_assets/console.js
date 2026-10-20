// SPDX-License-Identifier: GPL-3.0-or-later
(function () {
  "use strict";
  const el = (id) => document.getElementById(id),
    all = (s) => Array.from(document.querySelectorAll(s));
  const t = (key, values = {}) =>
    Object.entries(values).reduce(
      (s, [k, v]) => s.split("{" + k + "}").join(String(v)),
      TEXT[key] || key,
    );
  const esc = (v) =>
    String(v == null ? "" : v).replace(
      /[&<>"']/g,
      (c) =>
        ({
          "&": "&amp;",
          "<": "&lt;",
          ">": "&gt;",
          '"': "&quot;",
          "'": "&#39;",
        })[c],
    );
  all("[data-i18n]").forEach((n) => (n.textContent = t(n.dataset.i18n)));
  all("[data-placeholder]").forEach(
    (n) => (n.placeholder = t(n.dataset.placeholder)),
  );
  all("[data-aria]").forEach((n) =>
    n.setAttribute("aria-label", t(n.dataset.aria)),
  );
  all(".type-select").forEach(
    (n) =>
      (n.innerHTML =
        '<option value="0">' +
        esc(t("allTypes")) +
        "</option>" +
        Object.entries(TEXT.types)
          .filter(([k]) => k !== "0")
          .map(([k, v]) => '<option value="' + k + '">' + esc(v) + "</option>")
          .join("")),
  );
  ["itemRarity", "bulkRarity"].forEach((id) => {
    el(id).innerHTML = TEXT.rarities
      .map((s, i) => '<option value="' + i + '">' + esc(s) + "</option>")
      .join("");
    el(id).value = "5";
  });
  const RAW = {
    prefs: "rawPrefs",
    progress: "rawProgress",
    progress_document: "rawDocument",
    progress_slots: "rawSlots",
    client_state: "rawClientState",
  };
  Object.entries(RAW).forEach(([key, id]) =>
    el(id).setAttribute("aria-label", key),
  );
  let token = sessionStorage.getItem("opg3d_admin_token") || "",
    authEpoch = 0,
    connected = false,
    route = "overview",
    selected = "",
    player = null,
    snapshot = null;
  let tab = "profile",
    dirty = false,
    playerSeq = 0,
    listSeq = 0,
    catalogSeq = 0,
    ownedPage = 0,
    catalogOffset = 0,
    itemContext = null,
    bulkContext = null,
    busy = false;
  let stream = null,
    logLines = [];
  const base = (id) => "/api/v1/admin/players/" + encodeURIComponent(id);
  const status = (text, bad = false) => {
    el("status").textContent = text;
    el("status").hidden = !text;
    el("status").classList.toggle("bad", bad);
  };
  function connectedUI(value) {
    connected = value;
    el("connectionState").textContent = t(value ? "online" : "offline");
    el("connectionDot").classList.toggle("online", value);
  }
  async function api(method, path, body, signal) {
    if (!token) throw new Error(t("needConnection"));
    const epoch = authEpoch;
    let r;
    try {
      r = await fetch(path, {
        method,
        headers: {
          Authorization: "Bearer " + token,
          "Content-Type": "application/json",
        },
        body: body === undefined ? undefined : JSON.stringify(body),
        signal,
      });
    } catch (e) {
      if (e.name === "AbortError") throw e;
      throw new Error(t("networkError"));
    }
    if (epoch !== authEpoch) throw new DOMException("", "AbortError");
    const text = await r.text();
    if (epoch !== authEpoch) throw new DOMException("", "AbortError");
    let data;
    try {
      data = JSON.parse(text);
    } catch (_) {
      data = { detail: text.slice(0, 300) };
    }
    if (!r.ok) {
      const e = new Error(
        r.status === 401 || r.status === 403
          ? t("authError")
          : r.status === 409
            ? t("stale")
            : t("requestError", {
                status: r.status,
                detail: data.detail || data.error || "",
              }),
      );
      e.status = r.status;
      if (r.status === 401) {
        connectedUI(false);
        el("connectionDialog").showModal();
      }
      throw e;
    }
    return data;
  }
  const report = (e) => {
    if (e.name !== "AbortError") status(e.message, true);
  };
  function discard() {
    if (!dirty) return true;
    if (!confirm(t("discard"))) return false;
    dirty = false;
    if (player) fillPlayer();
    return true;
  }
  function debounce(fn) {
    let timer;
    return () => {
      clearTimeout(timer);
      timer = setTimeout(() => Promise.resolve(fn()).catch(report), 250);
    };
  }
  function searchEvents(input, button, fn) {
    const invoke = () => Promise.resolve(fn()).catch(report);
    el(input).addEventListener("input", debounce(fn));
    el(input).addEventListener("keydown", (e) => {
      if (e.key === "Enter") {
        e.preventDefault();
        invoke();
      }
    });
    el(button).onclick = invoke;
  }
  function setTab(value) {
    tab = value;
    all("#playerTabs [data-tab]").forEach((n) =>
      n.setAttribute("aria-selected", String(n.dataset.tab === tab)),
    );
    all(".tab-panel").forEach((n) => (n.hidden = n.id !== "tab-" + tab));
    if (tab === "inventory") renderOwned();
  }
  async function navigate(value) {
    if (!discard()) return;
    if (!["overview", "players", "catalog", "support", "logs"].includes(value))
      value = "overview";
    if (route === "logs" && value !== "logs") stopLogs();
    route = value;
    status("");
    window.scrollTo(0, 0);
    all(".page").forEach((n) => (n.hidden = n.id !== "page-" + route));
    all("[data-route]").forEach((n) =>
      n.setAttribute(
        "aria-current",
        n.dataset.route === route ? "page" : "false",
      ),
    );
    el("routeTitle").textContent = t(route);
    el("routeHint").textContent = t(
      {
        overview: "overviewHint",
        players: "playersHint",
        catalog: "catalogRouteHint",
        support: "supportHint",
        logs: "logsHint",
      }[route],
    );
    if (!connected) return;
    if (route === "overview") await loadStats();
    if (route === "players" && !selected) await loadPlayers();
    if (route === "catalog") {
      updateTarget();
      await searchCatalog();
    }
  }
  all("[data-route]").forEach(
    (n) => (n.onclick = () => navigate(n.dataset.route).catch(report)),
  );
  async function loadStats() {
    const d = await api("GET", "/api/v1/admin/stats"),
      s = d.stats || {};
    el("stats").innerHTML = [
      ["accounts", s.accounts],
      ["profiles", s.profiles],
      ["seenToday", s.seen_today],
      ["activeBans", s.active_bans],
    ]
      .map(
        ([key, v]) =>
          '<div class="card"><b>' +
          esc(v || 0) +
          "</b><span>" +
          esc(t(key)) +
          "</span></div>",
      )
      .join("");
  }
  async function loadPlayers() {
    const seq = ++listSeq;
    el("playersHint").textContent = t("loading");
    try {
      const d = await api(
        "GET",
        "/api/v1/admin/players?limit=300&q=" +
          encodeURIComponent(el("q").value.trim()) +
          "&banned=" +
          (el("onlyBanned").checked ? "1" : ""),
      );
      if (seq !== listSeq) return;
      el("players").innerHTML = (d.players || [])
        .map((p) => {
          const f = p.profile || {};
          return (
            "<tr><td><strong>" +
            esc(p.nick || f.nick || p.player_id) +
            "</strong><small>" +
            esc(p.player_id) +
            "</small></td><td>" +
            esc(f.level || 0) +
            "</td><td>" +
            esc(f.coins || 0) +
            "</td><td>" +
            esc(f.gems || 0) +
            "</td><td>" +
            esc(t(p.banned ? "blocked" : "normal")) +
            '</td><td><button data-player="' +
            esc(p.player_id) +
            '">' +
            esc(t("open")) +
            "</button></td></tr>"
          );
        })
        .join("");
      el("playersHint").textContent = t("listCount", {
        shown: (d.players || []).length,
        total: d.total || 0,
      });
      if (!(d.players || []).length)
        el("playersHint").textContent = t("emptyTitle");
      el("players")
        .querySelectorAll("[data-player]")
        .forEach(
          (n) => (n.onclick = () => showPlayer(n.dataset.player).catch(report)),
        );
    } catch (e) {
      if (seq === listSeq) el("playersHint").textContent = e.message;
      throw e;
    }
  }
  async function showPlayer(id, force = false) {
    if (!force && !discard()) return;
    const seq = ++playerSeq,
      changed = selected !== id;
    const [d, inv] = await Promise.all([
      api("GET", base(id)),
      api("GET", base(id) + "/inventory"),
    ]);
    if (seq !== playerSeq) return;
    player = d.player;
    selected = player.player_id;
    snapshot = inv;
    dirty = false;
    if (changed) {
      ownedPage = 0;
      el("ownedSearch").value = "";
      el("ownedType").value = "0";
      tab = "profile";
    }
    fillPlayer();
    el("playerList").hidden = true;
    el("playerDetail").hidden = false;
    setTab(tab);
    updateTarget();
    renderOwned();
  }
  function fillPlayer() {
    const f = player.profile || {};
    el("playerName").textContent = f.nick || player.nick || selected;
    el("playerID").textContent = selected;
    el("playerBan").textContent = t(player.banned ? "blocked" : "normal");
    el("playerBan").classList.toggle("bad", !!player.banned);
    [
      ["pfNick", f.nick || player.nick || ""],
      ["pfLevel", f.level || 1],
      ["pfExp", f.experience || 0],
      ["pfCoins", f.coins || 0],
      ["pfGems", f.gems || 0],
    ].forEach(([id, v]) => (el(id).value = v));
    Object.entries(RAW).forEach(
      ([key, id]) => (el(id).value = JSON.stringify(f[key] || {}, null, 2)),
    );
    el("metadata").textContent = JSON.stringify(player, null, 2);
    el("banDetails").textContent = player.banned
      ? t("banDetailsText", {
          kind: player.ban_kind || t("none"),
          reason: player.ban_reason || t("none"),
          until: player.ban_until || t("permanent"),
        })
      : t("normal");
    el("bnUndo").disabled = !player.banned;
  }
  function updateTarget() {
    el("catalogTarget").textContent =
      selected && player
        ? t("target", {
            id: selected,
            nick: player.nick || (player.profile || {}).nick || "",
          })
        : t("noPlayer");
  }
  const category = (item) =>
    TEXT.types[item.type] || item.category || TEXT.types["0"];
  function drawItems(items, host, owned) {
    const box = el(host);
    if (!items.length) {
      box.innerHTML =
        '<div class="empty"><strong>' +
        esc(t("emptyTitle")) +
        "</strong>" +
        esc(t("emptyHint")) +
        "</div>";
      return;
    }
    box.innerHTML =
      "<table><thead><tr>" +
      ["name", "category", "identifier", "actions"]
        .map((k) => "<th>" + esc(t(k)) + "</th>")
        .join("") +
      "</tr></thead><tbody>" +
      items
        .map((item, i) => {
          const has =
            snapshot &&
            (snapshot.items || []).some(
              (v) => v.type === item.type && v.index === item.index,
            );
          return (
            '<tr><td class="item-name"><strong>' +
            esc(item.name) +
            "</strong>" +
            (item.name_source === "technical_id"
              ? "<small>" + esc(t("technical")) + "</small>"
              : "") +
            (owned
              ? "<small>" +
                esc(
                  t("itemDetails", {
                    quantity: item.quantity,
                    level: item.level || "—",
                  }),
                ) +
                "</small>"
              : has
                ? "<small>" + esc(t("ownedBadge")) + "</small>"
                : "") +
            "</td><td>" +
            esc(category(item)) +
            '</td><td class="mono">' +
            esc(item.index) +
            "<small>" +
            esc(item.id) +
            '</small></td><td class="actions">' +
            (item.editable
              ? '<button data-item="' +
                i +
                '" data-action="grant" ' +
                (!selected || !snapshot ? "disabled" : "") +
                ">" +
                esc(t(owned ? "edit" : "grant")) +
                "</button>" +
                (owned
                  ? '<button class="danger-outline" data-item="' +
                    i +
                    '" data-action="revoke" ' +
                    (item.equipped ? "disabled" : "") +
                    ">" +
                    esc(t("revoke")) +
                    "</button>"
                  : "") +
                (item.equipped
                  ? "<small>" + esc(t("equipped")) + "</small>"
                  : "")
              : "<small>" + esc(t("readOnly")) + "</small>") +
            "</td></tr>"
          );
        })
        .join("") +
      "</tbody></table>";
    box
      .querySelectorAll("[data-item]")
      .forEach(
        (n) =>
          (n.onclick = () =>
            openItem(items[Number(n.dataset.item)], n.dataset.action)),
      );
  }
  function renderOwned() {
    const source = snapshot ? snapshot.items || [] : [],
      terms = el("ownedSearch")
        .value.toLocaleLowerCase()
        .trim()
        .split(/\s+/)
        .filter(Boolean),
      typ = Number(el("ownedType").value);
    const rows = source.filter(
      (v) =>
        (!typ || v.type === typ) &&
        terms.every((term) =>
          (v.name + " " + v.id + " " + v.index)
            .toLocaleLowerCase()
            .includes(term),
        ),
    );
    ownedPage = Math.max(
      0,
      Math.min(ownedPage, Math.ceil(rows.length / 20) - 1),
    );
    const from = ownedPage * 20;
    drawItems(rows.slice(from, from + 20), "ownedItems", true);
    el("ownedCount").textContent = t("ownedCount", {
      found: rows.length,
      total: source.length,
    });
    el("ownedPage").textContent = t("pageCount", {
      from: rows.length ? from + 1 : 0,
      to: Math.min(from + 20, rows.length),
      total: rows.length,
    });
    el("ownedPrev").disabled = !ownedPage;
    el("ownedNext").disabled = from + 20 >= rows.length;
  }
  async function searchCatalog() {
    const seq = ++catalogSeq;
    el("catalogStatus").textContent = t("loading");
    try {
      const d = await api(
        "GET",
        "/api/v1/admin/catalog?q=" +
          encodeURIComponent(el("catalogSearch").value.trim()) +
          "&type=" +
          el("catalogType").value +
          "&offset=" +
          catalogOffset +
          "&limit=25",
      );
      if (seq !== catalogSeq) return;
      catalogOffset = d.offset || 0;
      drawItems(d.items || [], "catalogItems", false);
      el("catalogCount").textContent = t("pageCount", {
        from: d.total ? catalogOffset + 1 : 0,
        to: catalogOffset + (d.items || []).length,
        total: d.total,
      });
      el("catalogPrev").disabled = !catalogOffset;
      el("catalogNext").disabled = catalogOffset + 25 >= d.total;
      el("catalogStatus").textContent = "";
    } catch (e) {
      if (seq === catalogSeq) {
        el("catalogStatus").textContent = e.message;
        el("catalogItems").innerHTML = "";
        el("catalogCount").textContent = "";
        el("catalogPrev").disabled = true;
        el("catalogNext").disabled = true;
      }
    }
  }
  function openItem(item, action) {
    if (busy || !snapshot || !selected || !discard()) return;
    itemContext = {
      item,
      action,
      id: selected,
      revision: snapshot.revision,
      updated_at: snapshot.updated_at,
    };
    el("itemDialogTitle").textContent =
      t(action === "revoke" ? "revoke" : "grant") + " · " + item.name;
    el("itemTarget").textContent = t("target", {
      id: selected,
      nick: player.nick || "",
    });
    el("itemIdentity").textContent = t("itemCaption", {
      type: item.type,
      id: item.index,
      internal: item.id,
    });
    el("quantityField").hidden =
      action === "revoke" || ![1070, 1155, 1520].includes(item.type);
    el("levelField").hidden =
      action === "revoke" || ![10, 20, 70].includes(item.type);
    el("rarityField").hidden = action === "revoke" || item.type !== 10;
    el("itemQuantity").value = item.quantity || 1;
    el("itemLevel").value = item.level || 65;
    el("itemRarity").value = item.rarity == null ? 5 : item.rarity;
    el("itemWarning").textContent = t(
      action === "revoke" ? "revokeWarning" : "grantWarning",
    );
    el("itemError").textContent = "";
    el("itemDialog").showModal();
  }
  async function mutate(button, errorID, fn) {
    if (busy) return;
    busy = true;
    button.disabled = true;
    const locks = all("dialog input,dialog select");
    const states = locks.map((n) => n.disabled);
    locks.forEach((n) => (n.disabled = true));
    if (errorID) el(errorID).textContent = "";
    try {
      await fn();
    } catch (e) {
      if (errorID) el(errorID).textContent = e.message;
      else report(e);
    } finally {
      busy = false;
      locks.forEach((n, i) => (n.disabled = states[i]));
      button.disabled = false;
    }
  }
  el("itemForm").onsubmit = (e) => {
    e.preventDefault();
    const c = itemContext;
    if (!c) return;
    mutate(el("itemApply"), "itemError", async () => {
      const result = await api("POST", base(c.id) + "/inventory", {
        type: c.item.type,
        index: c.item.index,
        action: c.action,
        quantity: Number(el("itemQuantity").value),
        level: Number(el("itemLevel").value),
        rarity: Number(el("itemRarity").value),
        revision: c.revision,
        updated_at: c.updated_at,
      });
      el("itemDialog").close();
      if (selected === c.id) await showPlayer(c.id, true);
      status(t(result.changed ? "saved" : "unchanged"));
    });
  };
  function invalidateBulk() {
    if (bulkContext) bulkContext.plan = null;
    el("bulkPlan").hidden = true;
    el("bulkApply").disabled = true;
    el("bulkConfirm").value = "";
  }
  function openBulk(action) {
    if (busy || !selected || !snapshot || !discard()) return;
    bulkContext = { action, id: selected, plan: null };
    invalidateBulk();
    el("bulkTitle").textContent = t(
      {
        grant_weapons: "grantAllWeapons",
        revoke_weapons: "revokeAllWeapons",
        revoke_all: "revokeAll",
      }[action],
    );
    el("bulkTarget").textContent = t("target", {
      id: selected,
      nick: player.nick || "",
    });
    el("bulkDescription").textContent = t(
      action === "grant_weapons" ? "bulkGrantHint" : "bulkRevokeHint",
    );
    el("bulkFields").hidden = action !== "grant_weapons";
    el("bulkError").textContent = "";
    el("bulkDialog").showModal();
  }
  el("bulkPreview").onclick = () =>
    mutate(el("bulkPreview"), "bulkError", async () => {
      invalidateBulk();
      const c = bulkContext;
      const inv = await api("GET", base(c.id) + "/inventory");
      c.edit = {
        action: c.action,
        level: Number(el("bulkLevel").value),
        rarity: Number(el("bulkRarity").value),
        revision: inv.revision,
        updated_at: inv.updated_at,
      };
      const plan = await api(
        "POST",
        base(c.id) + "/inventory/bulk/preview",
        c.edit,
      );
      if (c !== bulkContext) return;
      c.plan = plan;
      el("bulkCounts").innerHTML = [
        ["changes", plan.changes],
        ["preserved", plan.preserved_owned],
        ["skippedEquipped", plan.skipped_equipped],
        ["skippedUnsupported", plan.skipped_unsupported],
      ]
        .map(
          ([key, n]) => "<div><b>" + esc(n) + "</b>" + esc(t(key)) + "</div>",
        )
        .join("");
      el("bulkSample").textContent = t("sample", {
        items:
          (plan.sample || [])
            .map((i) => i.name + " [" + i.index + "]")
            .join(", ") || "—",
      });
      el("bulkConfirmLabel").textContent = t("confirmID", {
        id: plan.player_id,
      });
      el("bulkPlan").hidden = false;
    });
  ["bulkLevel", "bulkRarity"].forEach(
    (id) => (el(id).oninput = invalidateBulk),
  );
  el("bulkConfirm").oninput = () =>
    (el("bulkApply").disabled =
      busy ||
      !bulkContext ||
      !bulkContext.plan ||
      !bulkContext.plan.changes ||
      el("bulkConfirm").value.trim() !== bulkContext.plan.player_id);
  el("bulkForm").onsubmit = (e) => {
    e.preventDefault();
    const c = bulkContext;
    if (
      !c ||
      !c.plan ||
      el("bulkConfirm").value.trim() !== c.plan.player_id ||
      !c.plan.changes
    )
      return;
    mutate(el("bulkApply"), "bulkError", async () => {
      try {
        const result = await api("POST", base(c.id) + "/inventory/bulk/apply", {
          ...c.edit,
          plan_token: c.plan.plan_token,
          confirm_player_id: el("bulkConfirm").value.trim(),
        });
        el("bulkDialog").close();
        if (selected === c.id) await showPlayer(c.id, true);
        status(
          t("bulkSaved", { count: result.changed ? result.plan.changes : 0 }),
        );
      } catch (err) {
        invalidateBulk();
        throw err;
      }
    }).then(() => el("bulkConfirm").oninput());
  };
  el("grantAllWeapons").onclick = () => openBulk("grant_weapons");
  el("revokeAllWeapons").onclick = () => openBulk("revoke_weapons");
  el("revokeAll").onclick = () => openBulk("revoke_all");
  el("profileForm").oninput = () => (dirty = true);
  Object.values(RAW).forEach((id) => (el(id).oninput = () => (dirty = true)));
  el("profileForm").onsubmit = (e) => {
    e.preventDefault();
    const id = selected;
    const data = {
      nick: el("pfNick").value.trim(),
      level: Number(el("pfLevel").value),
      experience: Number(el("pfExp").value),
      coins: Number(el("pfCoins").value),
      gems: Number(el("pfGems").value),
    };
    mutate(el("pfSave"), null, async () => {
      if (
        !Object.entries(data)
          .filter(([k]) => k !== "nick")
          .every(([, v]) => Number.isSafeInteger(v) && v >= 0) ||
        data.level < 1 ||
        data.level > 65
      )
        throw new Error(t("invalidNumbers"));
      await api("PUT", base(id) + "/profile", data);
      dirty = false;
      await showPlayer(id, true);
      status(t("saved"));
    });
  };
  el("rawSection").onchange = () =>
    Object.entries(RAW).forEach(
      ([key, id]) => (el(id).hidden = key !== el("rawSection").value),
    );
  el("showMetadata").onclick = () =>
    (el("metadata").hidden = !el("metadata").hidden);
  el("rawSave").onclick = () =>
    mutate(el("rawSave"), null, async () => {
      const id = selected,
        data = {};
      for (const [key, input] of Object.entries(RAW)) {
        let v;
        try {
          v = JSON.parse(el(input).value);
        } catch (_) {
          throw new Error(t("invalidJSON", { section: key }));
        }
        if (!v || Array.isArray(v) || typeof v !== "object")
          throw new Error(t("invalidJSON", { section: key }));
        data[key] = v;
      }
      if (!confirm(t("confirmRaw", { id }))) return;
      await api("PUT", base(id) + "/state", data);
      dirty = false;
      await showPlayer(id, true);
      status(t("saved"));
    });
  el("banForm").onsubmit = (e) => {
    e.preventDefault();
    const id = selected;
    if (!confirm(t("confirmBan", { id }))) return;
    mutate(el("bnDo"), null, async () => {
      await api("POST", base(id) + "/ban", {
        reason: el("bnReason").value,
        minutes: Number(el("bnMinutes").value),
        include_device: el("bnDevice").checked,
      });
      await showPlayer(id, true);
      status(t("operationSaved"));
    });
  };
  el("bnUndo").onclick = () => {
    const id = selected;
    if (!confirm(t("confirmUnban", { id }))) return;
    mutate(el("bnUndo"), null, async () => {
      await api("POST", base(id) + "/unban", {});
      await showPlayer(id, true);
      status(t("operationSaved"));
    });
  };
  el("pfDelete").onclick = () => {
    const id = selected;
    if (prompt(t("confirmDelete", { id })) !== id) return;
    mutate(el("pfDelete"), null, async () => {
      await api("DELETE", base(id));
      dirty = false;
      selected = "";
      snapshot = player = null;
      el("playerDetail").hidden = true;
      el("playerList").hidden = false;
      await loadPlayers();
      status(t("operationSaved"));
    });
  };
  all("[data-tab]").forEach((n) => (n.onclick = () => setTab(n.dataset.tab)));
  // Keyboard arrow navigation follows the visible tab order.
  el("playerTabs").onkeydown = (e) => {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
    e.preventDefault();
    const tabs = all("#playerTabs [data-tab]");
    let i = tabs.indexOf(document.activeElement);
    i =
      e.key === "Home"
        ? 0
        : e.key === "End"
          ? tabs.length - 1
          : (i + (e.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
    tabs[i].focus();
    setTab(tabs[i].dataset.tab);
  };
  all("[data-support]").forEach(
    (n) =>
      (n.onclick = () => {
        all("[data-support]").forEach((b) =>
          b.setAttribute("aria-selected", String(b === n)),
        );
        all('[id^="support-"]').forEach(
          (p) => (p.hidden = p.id !== "support-" + n.dataset.support),
        );
      }),
  );
  all("[data-support]")[0].setAttribute("aria-selected", "true");
  [
    [
      "doChangeId",
      "changeID",
      "cidOld",
      "cidNew",
      "change-id",
      "old_id",
      "new_id",
    ],
    [
      "doTransfer",
      "transfer",
      "trFrom",
      "trTo",
      "transfer",
      "from_id",
      "to_id",
    ],
    [
      "doReconcile",
      "reconcile",
      "rcSource",
      "rcTarget",
      "reconcile",
      "source_id",
      "target_id",
    ],
  ].forEach(
    ([button, key, a, b, path, ka, kb]) =>
      (el(button).onclick = () =>
        mutate(el(button), null, async () => {
          const from = el(a).value.trim(),
            to = el(b).value.trim();
          if (!from || !to || from === to) throw new Error(t("needIDs"));
          if (!confirm(t("confirmSupport", { action: t(key), from, to })))
            return;
          await api("POST", "/api/v1/admin/" + path, { [ka]: from, [kb]: to });
          selected = "";
          snapshot = player = null;
          el("playerDetail").hidden = true;
          el("playerList").hidden = false;
          updateTarget();
          status(t("operationSaved"));
        })),
  );
  function drawLogs() {
    const q = el("logFilter").value.toLocaleLowerCase();
    el("log").textContent = logLines
      .filter((s) => s.toLocaleLowerCase().includes(q))
      .join("\n");
    el("log").scrollTop = el("log").scrollHeight;
  }
  function stopLogs() {
    if (stream) stream.abort();
    stream = null;
    el("logStart").disabled = false;
    el("logStop").disabled = true;
  }
  el("logStart").onclick = async () => {
    if (stream) return;
    const ctrl = new AbortController();
    stream = ctrl;
    el("logStart").disabled = true;
    el("logStop").disabled = false;
    try {
      if (!token) throw new Error(t("needConnection"));
      const r = await fetch("/api/v1/admin/logs/stream", {
        headers: { Authorization: "Bearer " + token },
        signal: ctrl.signal,
      });
      if (!r.ok)
        throw new Error(t("requestError", { status: r.status, detail: "" }));
      const reader = r.body.getReader(),
        decoder = new TextDecoder();
      let pending = "";
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        pending += decoder.decode(value, { stream: true });
        let end;
        while ((end = pending.indexOf("\n")) >= 0) {
          const line = pending.slice(0, end).trim();
          pending = pending.slice(end + 1);
          if (line.startsWith("data:")) {
            try {
              const entry = JSON.parse(line.slice(5));
              logLines.push(entry.line || JSON.stringify(entry));
            } catch (_) {
              logLines.push(line.slice(5));
            }
          }
        }
        if (pending.length > 1048576) pending = "";
        logLines = logLines.slice(-2000);
        drawLogs();
      }
    } catch (e) {
      if (e.name !== "AbortError") report(e);
    } finally {
      if (stream === ctrl) stopLogs();
    }
  };
  el("logStop").onclick = stopLogs;
  el("logClear").onclick = () => {
    logLines = [];
    drawLogs();
  };
  el("logFilter").oninput = drawLogs;
  searchEvents("q", "find", loadPlayers);
  el("onlyBanned").onchange = () => loadPlayers().catch(report);
  searchEvents("catalogSearch", "catalogFind", () => {
    catalogOffset = 0;
    return searchCatalog();
  });
  el("catalogType").onchange = () => {
    catalogOffset = 0;
    searchCatalog().catch(report);
  };
  el("ownedSearch").oninput = () => {
    ownedPage = 0;
    renderOwned();
  };
  el("ownedType").onchange = () => {
    ownedPage = 0;
    renderOwned();
  };
  el("ownedPrev").onclick = () => {
    ownedPage--;
    renderOwned();
  };
  el("ownedNext").onclick = () => {
    ownedPage++;
    renderOwned();
  };
  el("catalogPrev").onclick = () => {
    catalogOffset = Math.max(0, catalogOffset - 25);
    searchCatalog().catch(report);
  };
  el("catalogNext").onclick = () => {
    catalogOffset += 25;
    searchCatalog().catch(report);
  };
  el("ownedReload").onclick = () => {
    if (discard()) showPlayer(selected, true).catch(report);
  };
  el("openCatalog").onclick = () => navigate("catalog").catch(report);
  el("choosePlayer").onclick = () => {
    if (!discard()) return;
    el("playerDetail").hidden = true;
    el("playerList").hidden = false;
    navigate("players").then(loadPlayers).catch(report);
  };
  el("backPlayers").onclick = () => {
    if (!discard()) return;
    selected = "";
    ++playerSeq;
    snapshot = player = null;
    el("playerDetail").hidden = true;
    el("playerList").hidden = false;
    loadPlayers().catch(report);
  };
  el("refresh").onclick = () => {
    if (!discard()) return;
    (route === "players" && selected
      ? showPlayer(selected, true)
      : navigate(route)
    ).catch(report);
  };
  all("[data-close]").forEach(
    (n) =>
      (n.onclick = () => {
        if (!busy) el(n.dataset.close).close();
      }),
  );
  all("dialog").forEach((n) =>
    n.addEventListener("cancel", (e) => {
      if (busy) e.preventDefault();
    }),
  );
  el("connectionOpen").onclick = () => el("connectionDialog").showModal();
  el("connectionForm").onsubmit = (e) => {
    e.preventDefault();
    mutate(el("connect"), "connectionError", async () => {
      token = el("token").value.trim();
      ++authEpoch;
      await loadStats();
      sessionStorage.setItem("opg3d_admin_token", token);
      el("token").value = "";
      connectedUI(true);
      el("connectionDialog").close();
      await navigate(route);
    });
  };
  el("logout").onclick = () => {
    if (busy || !discard()) return;
    ++authEpoch;
    ++playerSeq;
    ++catalogSeq;
    ++listSeq;
    stopLogs();
    token = "";
    sessionStorage.removeItem("opg3d_admin_token");
    selected = "";
    snapshot = player = null;
    connectedUI(false);
    logLines = [];
    drawLogs();
    ["players", "stats", "ownedItems", "catalogItems", "metadata"].forEach(
      (id) => (el(id).textContent = ""),
    );
    Object.values(RAW).forEach((id) => (el(id).value = ""));
    el("profileForm").reset();
    el("playerDetail").hidden = true;
    el("playerList").hidden = false;
    updateTarget();
    status("");
    el("connectionError").textContent = "";
    el("connectionDialog").showModal();
  };
  window.addEventListener("beforeunload", (e) => {
    if (dirty) {
      e.preventDefault();
      e.returnValue = "";
    }
  });
  // Fit the active table below its controls, keeping pagination within reach.
  let fitFrame = 0;
  function fitTables() {
    cancelAnimationFrame(fitFrame);
    fitFrame = requestAnimationFrame(() =>
      all(".table-wrap").forEach((n) => {
        if (!n.getClientRects().length) return;
        const top = n.getBoundingClientRect().top + window.scrollY;
        const height =
          window.innerWidth <= 640
            ? 480
            : Math.max(200, window.innerHeight - top - 132);
        n.style.maxHeight = height + "px";
      }),
    );
  }
  new ResizeObserver(fitTables).observe(document.querySelector("main"));
  window.addEventListener("resize", fitTables);
  navigate("overview");
  if (token) {
    loadStats()
      .then(() => connectedUI(true))
      .catch(report);
  } else el("connectionDialog").showModal();
})();
