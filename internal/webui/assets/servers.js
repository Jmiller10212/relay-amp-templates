import {api} from "/api.js?v=0.7.4";

export function createServersUI(select, friendsUI, notify, navigation, activateConversation) {
  let servers = [], invites = [], selected = null, members = [], pins = [];
  let notificationMode = "all", searchCursor = 0, searchParams = null;
  const notificationModes = new Map();
  const channelActivity = new Map();
  let memberPanelWanted = localStorage.getItem("relay.memberPanelVisible") !== "false";
  const enc = encodeURIComponent;
  const initials = (name = "") => name.trim().split(/\s+/).slice(0, 2).map((part) => part[0] || "").join("").toUpperCase() || "S";
  const channel = (server) => server?.channels?.[0];
  const selectedChannel = () => channel(selected);

  function action(label, handler, quiet = false) {
    const button = document.createElement("button");
    button.type = "button"; button.className = quiet ? "secondary" : "primary"; button.textContent = label;
    button.addEventListener("click", async () => { button.disabled = true; try { await handler(); } catch (error) { notify(error.message, "error"); } finally { button.disabled = false; } });
    return button;
  }

  function renderRail() {
    const rail = select("#server-rail"); rail.textContent = "";
    servers.forEach((server) => {
      const button = document.createElement("button");
      button.type = "button"; button.className = `rail-button ${selected?.id === server.id ? "active" : ""}`;
      const activity = (server.channels || []).reduce((total, item) => total + (channelActivity.get(item.conversationId)?.count || 0), 0);
      const mentioned = (server.channels || []).some((item) => channelActivity.get(item.conversationId)?.mentioned);
      button.title = server.name; button.setAttribute("aria-label", `${server.name}${activity ? `, ${activity} unread` : ""}`);
      const label = document.createElement("span"); label.textContent = initials(server.name); button.append(label);
      if (activity) { const badge = document.createElement("b"); badge.className = "rail-badge"; badge.textContent = activity > 99 ? "99+" : String(activity); button.append(badge); }
      button.classList.toggle("mention-pulse", mentioned);
      button.addEventListener("click", () => open(server.id)); rail.append(button);
    });
  }

  function renderSelectedChannelActivity() {
    const conversationID = selectedChannel()?.conversationId, activity = channelActivity.get(conversationID);
    const button = select("#server-general-button"), badge = select("#server-general-badge");
    const count = activity?.count || 0; badge.textContent = count > 99 ? "99+" : String(count); badge.hidden = !count;
    button.classList.toggle("mention-pulse", Boolean(activity?.mentioned));
  }

  function recordChannelActivity(conversationID, mentioned, mode) {
    if (mode === "nothing" || (mode === "mentions" && !mentioned)) return;
    const activity = channelActivity.get(conversationID) || {count: 0, mentioned: false};
    activity.count += 1; activity.mentioned ||= mentioned; channelActivity.set(conversationID, activity);
    renderRail(); renderSelectedChannelActivity();
  }

  function markConversationSeen(conversationID) {
    if (!conversationID || !channelActivity.delete(conversationID)) return;
    renderRail(); renderSelectedChannelActivity();
  }

  function renderInvites() {
    const list = select("#server-invite-list"); list.textContent = "";
    select("#server-invite-count").textContent = String(invites.length);
    const badge = select("#server-invites-badge"); badge.textContent = invites.length > 99 ? "99+" : String(invites.length); badge.hidden = !invites.length;
    invites.forEach((invite) => {
      const row = document.createElement("div"); row.className = "server-invite-row";
      const avatar = document.createElement("span"); avatar.className = "friend-avatar"; avatar.textContent = initials(invite.server.name);
      const copy = document.createElement("div"), title = document.createElement("strong"), detail = document.createElement("small");
      title.textContent = invite.server.name; detail.textContent = `Invited by ${invite.inviter.displayName} (@${invite.inviter.username})`; copy.append(title, detail);
      const actions = document.createElement("div"); actions.className = "row-actions";
      actions.append(action("Accept", async () => { await api(`/api/v1/server-invites/${enc(invite.id)}/accept`, {method: "POST", body: "{}"}); await load(); }), action("Decline", async () => { await api(`/api/v1/server-invites/${enc(invite.id)}/decline`, {method: "POST", body: "{}"}); await load(); }, true));
      row.append(avatar, copy, actions); list.append(row);
    });
    if (!invites.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "No pending server invitations."; list.append(empty); }
  }

  async function load(seed) {
    if (seed) { servers = seed.servers || []; invites = seed.serverInvites || []; }
    else {
      const [serverResult, inviteResult] = await Promise.all([api("/api/v1/servers", {headers: {}}), api("/api/v1/server-invites", {headers: {}})]);
      servers = serverResult.servers || []; invites = inviteResult.invites || [];
    }
    if (selected) selected = servers.find((item) => item.id === selected.id) || null;
    renderRail(); renderInvites(); return {servers, invites};
  }

  function closeMenu(focus = false) {
    const menu = select("#server-menu"); if (menu.hidden) return;
    menu.hidden = true; select("#server-menu-button").setAttribute("aria-expanded", "false");
    if (focus) select("#server-menu-button").focus();
  }

  function configureMenu() {
    const owner = selected?.role === "owner";
    select("#server-menu-settings").hidden = !owner;
    const leave = select("#server-menu-leave"); leave.disabled = owner;
    leave.title = owner ? "Transfer ownership or delete the server first." : "Leave this server";
    leave.setAttribute("aria-disabled", String(owner));
  }

  function toggleMenu() {
    if (!selected) return;
    const menu = select("#server-menu"), show = menu.hidden; configureMenu(); menu.hidden = !show;
    select("#server-menu-button").setAttribute("aria-expanded", String(show));
    if (show) menu.querySelector("button:not([hidden]):not(:disabled)")?.focus();
  }

  function conversationForSelected() {
    const item = selectedChannel();
    return item ? {id: item.conversationId, channelId: item.id, kind: "channel", name: item.name, serverId: selected.id, serverName: selected.name, canSend: true} : null;
  }

  async function open(serverID, options = {}) {
    closeMenu(); selected = servers.find((item) => item.id === serverID); if (!selected) return;
    select("#server-context-name").textContent = selected.name;
    select("#search-scope-copy").textContent = selected.name;
    select("#server-search-input").placeholder = `Search ${selected.name}`;
    configureMenu(); navigation.destination("server", selected.id); renderRail();
    await Promise.all([refreshMembers(), loadPins(), loadNotificationPreference()]);
    const conversation = conversationForSelected();
    if (conversation) {
      await activateConversation(conversation, options);
      if (document.visibilityState === "visible") markConversationSeen(conversation.id);
      else renderSelectedChannelActivity();
      applyMemberPanel();
    }
  }

  function showInvitations() { closeMenu(); deactivateChannel(); navigation.destination("home"); navigation.invitations(); renderInvites(); }
  function memberSort(a, b) { return a.user.displayName.localeCompare(b.user.displayName, undefined, {sensitivity: "base"}); }

  async function refreshMembers() {
    if (!selected) return;
    const result = await api(`/api/v1/servers/${enc(selected.id)}/members`, {headers: {}}); members = result.members || [];
    renderMemberPanel(); renderSettingsMembers();
  }

  function memberRow(member, management = false) {
    const row = document.createElement("div"); row.className = management ? "server-member-row" : "member-panel-row";
    const avatar = document.createElement("span"); avatar.className = "friend-avatar"; avatar.textContent = initials(member.user.displayName);
    const dot = document.createElement("i"); dot.className = `presence-dot ${member.online ? "online" : ""}`; avatar.append(dot);
    const copy = document.createElement("div"), title = document.createElement("strong"), detail = document.createElement("small");
    title.textContent = member.user.displayName; detail.textContent = `@${member.user.username}`; copy.append(title, detail);
    const controls = document.createElement("div"); controls.className = "row-actions";
    if (member.role === "owner") { const role = document.createElement("span"); role.className = "server-role"; role.textContent = "Owner"; controls.append(role); }
    if (management && selected?.role === "owner" && member.role !== "owner") controls.append(action("Remove", async () => { await api(`/api/v1/servers/${enc(selected.id)}/members/${enc(member.user.id)}`, {method: "DELETE"}); await refreshMembers(); }, true));
    row.append(avatar, copy, controls); return row;
  }

  function renderMemberPanel() {
    const container = select("#member-panel-content"); container.textContent = "";
    for (const [label, group] of [["Online", members.filter((item) => item.online).sort(memberSort)], ["Offline", members.filter((item) => !item.online).sort(memberSort)]]) {
      const heading = document.createElement("div"); heading.className = "member-group-title"; heading.textContent = `${label} — ${group.length}`; container.append(heading);
      group.forEach((member) => container.append(memberRow(member)));
    }
  }

  function renderSettingsMembers() {
    const list = select("#server-settings-members"); if (!list) return; list.textContent = "";
    members.forEach((member) => list.append(memberRow(member, true)));
  }

  function applyMemberPanel() {
    const visible = Boolean(selected && memberPanelWanted);
    select("#member-panel").hidden = !visible; select("#chat-shell").classList.toggle("members-open", visible);
    const button = select("#member-panel-button"); button.classList.toggle("active", visible); button.setAttribute("aria-pressed", String(visible));
    const label = visible ? "Hide Member List" : "Show Member List"; button.title = label; button.setAttribute("aria-label", label);
  }

  function toggleMemberPanel() { memberPanelWanted = !memberPanelWanted; localStorage.setItem("relay.memberPanelVisible", String(memberPanelWanted)); applyMemberPanel(); }
  function deactivateChannel() { closeMenu(); select("#server-channel-tools").hidden = true; select("#member-panel").hidden = true; select("#chat-shell").classList.remove("members-open"); select("#server-search-panel").hidden = true; }

  function showInvite() {
    if (!selected) return;
    const list = select("#server-invite-friends"), query = select("#server-invite-search").value.trim().toLowerCase(); list.textContent = "";
    friendsUI.getFriends().filter((friend) => !query || friend.displayName.toLowerCase().includes(query) || friend.username.toLowerCase().includes(query)).forEach((friend) => {
      const row = document.createElement("button"); row.type = "button"; row.className = "modal-friend";
      const avatar = document.createElement("span"); avatar.className = "friend-avatar"; avatar.textContent = initials(friend.displayName);
      const copy = document.createElement("span"); copy.className = "dm-copy";
      const title = document.createElement("strong"); title.textContent = friend.displayName;
      const detail = document.createElement("small"); detail.textContent = `@${friend.username}`; copy.append(title, detail); row.append(avatar, copy);
      row.addEventListener("click", async () => { try { await api(`/api/v1/servers/${enc(selected.id)}/invites`, {method: "POST", body: JSON.stringify({userId: friend.id})}); select("#server-invite-dialog").close(); notify("Server invitation sent.", "success"); } catch (error) { notify(error.message, "error"); } });
      list.append(row);
    });
    if (!select("#server-invite-dialog").open) select("#server-invite-dialog").showModal();
  }

  async function showSettings() {
    if (!selected || selected.role !== "owner") return;
    await refreshMembers(); select("#server-settings-name").textContent = selected.name;
    select("#server-leave-button").closest("section").hidden = true;
    select("#server-rename-form").elements.name.value = selected.name; select("#server-delete-form").elements.name.value = "";
    const choices = select("#server-transfer-form").elements.userId; choices.textContent = "";
    members.filter((member) => member.role !== "owner").forEach((member) => { const option = document.createElement("option"); option.value = member.user.id; option.textContent = `${member.user.displayName} (@${member.user.username})`; choices.append(option); });
    select("#server-transfer-form").querySelector("button").disabled = !choices.options.length;
    select("#server-settings-dialog").showModal();
  }

  async function leaveServer() {
    if (!selected || selected.role === "owner") return;
    await api(`/api/v1/servers/${enc(selected.id)}/leave`, {method: "POST", body: "{}"});
    selected = null; await load(); deactivateChannel(); navigation.destination("home"); navigation.friends();
  }

  async function loadPins() {
    const item = selectedChannel(); if (!item) { pins = []; return; }
    const result = await api(`/api/v1/channels/${enc(item.id)}/pins`, {headers: {}}); pins = result.pins || []; renderPins();
  }

  function renderPins() {
    const list = select("#channel-pins-list"); list.textContent = "";
    if (!pins.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "No messages are pinned in this channel."; list.append(empty); return; }
    pins.forEach((pin) => {
      const row = document.createElement("article"); row.className = "channel-pin-row";
      const meta = document.createElement("div"); meta.className = "pin-meta";
      const author = document.createElement("strong"); author.textContent = pin.message.displayName || pin.message.username;
      const time = document.createElement("time"); time.dateTime = pin.pinnedAt; time.textContent = new Date(pin.pinnedAt).toLocaleString(); meta.append(author, time);
      const text = document.createElement("p"); text.textContent = pin.message.text; row.append(meta, text);
      if (selected?.role === "owner") row.append(action("Unpin", async () => togglePin(pin.message.id), true));
      row.addEventListener("click", (event) => { if (event.target.closest("button")) return; select("#channel-pins-dialog").close(); activateConversation(conversationForSelected(), {targetMessageId: pin.message.id}); });
      list.append(row);
    });
  }

  function isPinned(messageID) { return pins.some((pin) => Number(pin.message.id) === Number(messageID)); }
  async function togglePin(messageID) {
    const item = selectedChannel(); if (!item || selected?.role !== "owner") return;
    const path = `/api/v1/channels/${enc(item.id)}/pins/${enc(messageID)}`;
    await api(path, isPinned(messageID) ? {method: "DELETE"} : {method: "PUT", body: "{}"}); await loadPins();
  }

  async function loadNotificationPreference() {
    const item = selectedChannel(); if (!item) return;
    const result = await api(`/api/v1/channels/${enc(item.id)}/notification-preference`, {headers: {}}); notificationMode = result.preference?.mode || "all"; notificationModes.set(item.id, notificationMode);
  }
  function showNotificationSettings() { const input = select(`#channel-notifications-form input[value="${notificationMode}"]`); if (input) input.checked = true; select("#channel-notifications-dialog").showModal(); }
  async function notificationModeForConversation(conversationID) {
    const entry = servers.flatMap((server) => (server.channels || []).map((item) => ({server, channel: item}))).find((item) => item.channel.conversationId === conversationID);
    if (!entry) return "nothing";
    if (notificationModes.has(entry.channel.id)) return notificationModes.get(entry.channel.id);
    const result = await api(`/api/v1/channels/${enc(entry.channel.id)}/notification-preference`, {headers: {}});
    const mode = result.preference?.mode || "all"; notificationModes.set(entry.channel.id, mode); return mode;
  }

  function parseSearch(value) {
    const params = {q: ""}, text = [];
    for (const token of value.trim().split(/\s+/).filter(Boolean)) {
      const match = token.match(/^(from|in|mentions):(.+)$/i); if (!match) { text.push(token); continue; }
      const kind = match[1].toLowerCase(), name = match[2].toLowerCase().replace(/^[@#]/, "");
      if (kind === "in") {
        const found = selected?.channels?.find((item) => item.name.toLowerCase() === name); if (!found) throw new Error(`No #${name} channel exists in this server.`); params.channelId = found.id;
      } else {
        const found = members.find((member) => member.user.username.toLowerCase() === name); if (!found) throw new Error(`No @${name} member exists in this server.`); params[kind === "from" ? "fromUserId" : "mentionsUserId"] = found.user.id;
      }
    }
    params.q = text.join(" "); return params;
  }

  function renderSearchSuggestions() {
    const list = select("#server-search-suggestions"), input = select("#server-search-input"); list.textContent = "";
    const tail = input.value.match(/(?:^|\s)(from:|mentions:|in:)([^\s]*)$/i); if (!tail) return;
    const prefix = tail[1].toLowerCase(), query = tail[2].replace(/^[@#]/, "").toLowerCase();
    const values = prefix === "in:" ? (selected?.channels || []).map((item) => item.name) : members.map((member) => member.user.username);
    values.filter((value) => value.toLowerCase().includes(query)).slice(0, 8).forEach((value) => {
      const button = document.createElement("button"); button.type = "button"; button.textContent = `${prefix}${value}`;
      button.addEventListener("click", () => { input.value = input.value.slice(0, input.value.length - tail[0].trimStart().length) + `${prefix}${value} `; list.textContent = ""; input.focus(); }); list.append(button);
    });
  }

  async function performSearch(append = false) {
    if (!selected) return; const status = select("#server-search-status");
    try {
      if (!append) { searchParams = parseSearch(select("#server-search-input").value); searchCursor = 0; select("#server-search-results").textContent = ""; }
      status.textContent = "Searching…"; const query = new URLSearchParams({limit: "25"});
      Object.entries(searchParams || {}).forEach(([key, value]) => { if (value) query.set(key, String(value)); }); if (append && searchCursor) query.set("before", String(searchCursor));
      const result = await api(`/api/v1/servers/${enc(selected.id)}/search?${query}`, {headers: {}});
      renderSearchResults(result.results || [], append); searchCursor = result.nextBefore || 0; select("#server-search-more").hidden = !result.hasMore;
      status.textContent = (result.results || []).length ? "" : (append ? "No more results." : "No matching messages."); select("#server-search-panel").hidden = false;
    } catch (error) { status.textContent = error.message; select("#server-search-panel").hidden = false; }
  }

  function renderSearchResults(results, append) {
    const list = select("#server-search-results"); if (!append) list.textContent = "";
    results.forEach((result) => {
      const button = document.createElement("button"); button.type = "button"; button.className = "search-result";
      const meta = document.createElement("span"); meta.className = "search-result-meta"; meta.textContent = `#${result.channel.name} · ${result.message.displayName || result.message.username} · ${new Date(result.message.createdAt).toLocaleString()}`;
      const text = document.createElement("span"); text.textContent = result.message.text; button.append(meta, text);
      button.addEventListener("click", async () => { select("#server-search-panel").hidden = true; await activateConversation({...conversationForSelected(), id: result.message.conversationId, channelId: result.channel.id, name: result.channel.name}, {targetMessageId: result.message.id}); }); list.append(button);
    });
  }

  async function handleRealtime(event) {
    if (!selected) return;
    if (["presence.changed", "profile.updated", "server.membership.changed", "server.updated"].includes(event.type)) await refreshMembers().catch(() => {});
    if (event.type === "channel.pin.created" || event.type === "channel.pin.removed") await loadPins().catch(() => {});
  }

  select("#add-server-button").addEventListener("click", () => select("#server-create-dialog").showModal());
  select("#server-create-invites").addEventListener("click", () => { select("#server-create-dialog").close(); showInvitations(); });
  select("#server-invites-destination").addEventListener("click", showInvitations);
  select("#server-general-button").addEventListener("click", () => selected && open(selected.id));
  select("#server-menu-button").addEventListener("click", toggleMenu);
  select("#server-menu-invite").addEventListener("click", () => { closeMenu(); showInvite(); });
  select("#server-menu-settings").addEventListener("click", () => { closeMenu(); showSettings().catch((error) => notify(error.message, "error")); });
  select("#server-menu-leave").addEventListener("click", () => { closeMenu(); leaveServer().catch((error) => notify(error.message, "error")); });
  select("#server-invite-search").addEventListener("input", showInvite);
  select("#member-panel-button").addEventListener("click", toggleMemberPanel); select("#member-panel-close").addEventListener("click", toggleMemberPanel);
  select("#channel-pins-button").addEventListener("click", async () => { try { await loadPins(); select("#channel-pins-dialog").showModal(); } catch (error) { notify(error.message, "error"); } });
  select("#channel-notifications-button").addEventListener("click", showNotificationSettings);
  select("#server-search-close").addEventListener("click", () => { select("#server-search-panel").hidden = true; });
  select("#server-search-input").addEventListener("input", renderSearchSuggestions);
  select("#server-search-form").addEventListener("submit", (event) => { event.preventDefault(); performSearch(); });
  select("#server-search-more").addEventListener("click", () => performSearch(true));
  document.querySelectorAll("[data-search-token]").forEach((button) => button.addEventListener("click", () => { const input = select("#server-search-input"); input.value += `${input.value ? " " : ""}${button.dataset.searchToken}`; input.focus(); renderSearchSuggestions(); }));
  document.addEventListener("click", (event) => { if (!event.target.closest("#server-menu") && !event.target.closest("#server-menu-button")) closeMenu(); });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") { closeMenu(true); select("#server-search-panel").hidden = true; }
    if (!select("#server-menu").hidden && ["ArrowDown", "ArrowUp"].includes(event.key)) { event.preventDefault(); const items = [...select("#server-menu").querySelectorAll("button:not([hidden]):not(:disabled)")], current = items.indexOf(document.activeElement); items[(current + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length]?.focus(); }
  });
  document.querySelectorAll("[data-close-dialog]").forEach((button) => button.addEventListener("click", () => select(`#${button.dataset.closeDialog}`).close()));
  select("#channel-notifications-form").addEventListener("submit", async (event) => { event.preventDefault(); const item = selectedChannel(); if (!item) return; try { const mode = new FormData(event.currentTarget).get("mode"), result = await api(`/api/v1/channels/${enc(item.id)}/notification-preference`, {method: "PUT", body: JSON.stringify({mode})}); notificationMode = result.preference.mode; notificationModes.set(item.id, notificationMode); select("#channel-notifications-dialog").close(); notify("Channel notification setting saved.", "success"); } catch (error) { notify(error.message, "error"); } });
  select("#server-create-form").addEventListener("submit", async (event) => { event.preventDefault(); const form = event.currentTarget; try { const result = await api("/api/v1/servers", {method: "POST", body: JSON.stringify({name: form.elements.name.value})}); form.reset(); select("#server-create-dialog").close(); await load(); await open(result.server.id); } catch (error) { notify(error.message, "error"); } });
  select("#server-rename-form").addEventListener("submit", async (event) => { event.preventDefault(); try { await api(`/api/v1/servers/${enc(selected.id)}`, {method: "PATCH", body: JSON.stringify({name: event.currentTarget.elements.name.value})}); await load(); await open(selected.id); select("#server-settings-dialog").close(); } catch (error) { notify(error.message, "error"); } });
  select("#server-transfer-form").addEventListener("submit", async (event) => { event.preventDefault(); try { await api(`/api/v1/servers/${enc(selected.id)}/ownership`, {method: "POST", body: JSON.stringify({userId: event.currentTarget.elements.userId.value, confirm: event.currentTarget.elements.confirm.checked})}); await load(); select("#server-settings-dialog").close(); } catch (error) { notify(error.message, "error"); } });
  select("#server-leave-button").addEventListener("click", () => leaveServer().catch((error) => notify(error.message, "error")));
  select("#server-delete-form").addEventListener("submit", async (event) => { event.preventDefault(); try { await api(`/api/v1/servers/${enc(selected.id)}`, {method: "DELETE", body: JSON.stringify({name: event.currentTarget.elements.name.value})}); select("#server-settings-dialog").close(); selected = null; await load(); deactivateChannel(); navigation.destination("home"); navigation.friends(); } catch (error) { notify(error.message, "error"); } });

  return {load, open, showInvitations, refreshMembers, handleRealtime, togglePin, isPinned, deactivateChannel, invitationCount: () => invites.length, getServers: () => servers, selected: () => selected, notificationModeForConversation, recordChannelActivity, markConversationSeen, mentionCandidates: () => members.map((member) => member.user), channelForConversation: (conversationID) => servers.flatMap((server) => (server.channels || []).map((item) => ({server, channel: item}))).find((entry) => entry.channel.conversationId === conversationID)};
}
