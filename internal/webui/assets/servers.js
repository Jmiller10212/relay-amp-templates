import {api} from "/api.js?v=0.7.0";

export function createServersUI(select, friendsUI, notify, navigation, activateConversation) {
  let servers = [];
  let invites = [];
  let selected = null;
  let members = [];

  const initials = (name) => name.trim().split(/\s+/).slice(0, 2).map((part) => part[0] || "").join("").toUpperCase() || "S";
  const channel = (server) => server.channels?.[0];

  function renderRail() {
    const rail = select("#server-rail");
    rail.textContent = "";
    servers.forEach((server) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = `rail-button ${selected?.id === server.id ? "active" : ""}`;
      button.dataset.serverRail = server.id;
      button.title = server.name;
      button.setAttribute("aria-label", server.name);
      button.textContent = initials(server.name);
      button.addEventListener("click", () => open(server.id));
      rail.append(button);
    });
  }

  function renderInvites() {
    const list = select("#server-invite-list");
    list.textContent = "";
    select("#server-invite-count").textContent = String(invites.length);
    const badge = select("#server-invites-badge");
    badge.textContent = invites.length > 99 ? "99+" : String(invites.length);
    badge.hidden = invites.length === 0;
    invites.forEach((invite) => {
      const row = document.createElement("div"); row.className = "server-invite-row";
      const avatar = document.createElement("span"); avatar.className = "friend-avatar"; avatar.textContent = initials(invite.server.name);
      const copy = document.createElement("div"); const title = document.createElement("strong"); const detail = document.createElement("small");
      title.textContent = invite.server.name; detail.textContent = `Invited by ${invite.inviter.displayName} (@${invite.inviter.username})`; copy.append(title, detail);
      const actions = document.createElement("div"); actions.className = "row-actions";
      actions.append(action("Accept", async () => { await api(`/api/v1/server-invites/${encodeURIComponent(invite.id)}/accept`, {method: "POST", body: "{}"}); await load(); }), action("Decline", async () => { await api(`/api/v1/server-invites/${encodeURIComponent(invite.id)}/decline`, {method: "POST", body: "{}"}); await load(); }, true));
      row.append(avatar, copy, actions); list.append(row);
    });
    if (!invites.length) { const empty = document.createElement("p"); empty.className = "empty"; empty.textContent = "No pending server invitations."; list.append(empty); }
  }

  function action(label, handler, quiet = false) {
    const button = document.createElement("button"); button.type = "button"; button.className = quiet ? "secondary" : "primary"; button.textContent = label;
    button.addEventListener("click", async () => { button.disabled = true; try { await handler(); } catch (error) { notify(error.message, "error"); } finally { button.disabled = false; } }); return button;
  }

  async function load(seed) {
    if (seed) { servers = seed.servers || []; invites = seed.serverInvites || []; }
    else {
      const [serverResult, inviteResult] = await Promise.all([api("/api/v1/servers", {headers: {}}), api("/api/v1/server-invites", {headers: {}})]);
      servers = serverResult.servers || []; invites = inviteResult.invites || [];
    }
    if (selected) selected = servers.find((item) => item.id === selected.id) || null;
    renderRail(); renderInvites();
    return {servers, invites};
  }

  async function open(serverID) {
    selected = servers.find((item) => item.id === serverID);
    if (!selected) return;
    const serverChannel = channel(selected);
    select("#server-context-name").textContent = selected.name;
    select("#server-context-mark").textContent = initials(selected.name);
    select("#server-settings-button").textContent = selected.role === "owner" ? "Server Settings" : "Leave Server";
    navigation.destination("server", selected.id); renderRail();
    if (serverChannel) await activateConversation({id: serverChannel.conversationId, channelId: serverChannel.id, kind: "channel", name: serverChannel.name, serverId: selected.id, serverName: selected.name, canSend: true});
  }

  function showInvitations() { navigation.destination("home"); navigation.invitations(); renderInvites(); }

  async function showMembers() {
    if (!selected) return;
    const result = await api(`/api/v1/servers/${encodeURIComponent(selected.id)}/members`, {headers: {}}); members = result.members || [];
    const list = select("#server-members-list"); list.textContent = ""; select("#server-members-copy").textContent = selected.name;
    members.forEach((member) => {
      const row = document.createElement("div"); row.className = "server-member-row";
      const avatar = document.createElement("span"); avatar.className = "friend-avatar"; avatar.textContent = initials(member.user.displayName); const dot = document.createElement("i"); dot.className = `presence-dot ${member.online ? "online" : ""}`; avatar.append(dot);
      const copy = document.createElement("div"); const title = document.createElement("strong"); const detail = document.createElement("small"); title.textContent = member.user.displayName; detail.textContent = `@${member.user.username} · ${member.online ? "Online" : "Offline"}`; copy.append(title, detail);
      const controls = document.createElement("div"); controls.className = "row-actions"; const role = document.createElement("span"); role.className = "server-role"; role.textContent = member.role; controls.append(role);
      if (selected.role === "owner" && member.role !== "owner") controls.append(action("Remove", async () => { await api(`/api/v1/servers/${encodeURIComponent(selected.id)}/members/${encodeURIComponent(member.user.id)}`, {method: "DELETE"}); await showMembers(); }, true));
      row.append(avatar, copy, controls); list.append(row);
    }); select("#server-members-dialog").showModal();
  }

  function showInvite() {
    if (!selected) return; const list = select("#server-invite-friends"); const query = select("#server-invite-search").value.trim().toLowerCase(); list.textContent = "";
    friendsUI.getFriends().filter((friend) => !query || friend.displayName.toLowerCase().includes(query) || friend.username.toLowerCase().includes(query)).forEach((friend) => {
      const row = document.createElement("button"); row.type = "button"; row.className = "modal-friend"; const avatar = document.createElement("span"); avatar.className = "friend-avatar"; avatar.textContent = initials(friend.displayName); const copy = document.createElement("span"); copy.className = "dm-copy"; const title = document.createElement("strong"); const detail = document.createElement("small"); title.textContent = friend.displayName; detail.textContent = `@${friend.username}`; copy.append(title, detail); row.append(avatar, copy);
      row.addEventListener("click", async () => { try { await api(`/api/v1/servers/${encodeURIComponent(selected.id)}/invites`, {method: "POST", body: JSON.stringify({userId: friend.id})}); select("#server-invite-dialog").close(); notify("Server invitation sent.", "success"); } catch (error) { notify(error.message, "error"); } }); list.append(row);
    }); if (!select("#server-invite-dialog").open) select("#server-invite-dialog").showModal();
  }

  async function showSettings() {
    if (!selected) return; await showMembersData(); const owner = selected.role === "owner";
    select("#server-settings-name").textContent = selected.name; select("#server-rename-form").hidden = !owner; select("#server-transfer-form").hidden = !owner; select("#server-delete-form").hidden = !owner; select("#server-leave-button").hidden = owner;
    select("#server-rename-form").elements.name.value = selected.name; select("#server-delete-form").elements.name.value = "";
    const choices = select("#server-transfer-form").elements.userId; choices.textContent = ""; members.filter((member) => member.role !== "owner").forEach((member) => { const option = document.createElement("option"); option.value = member.user.id; option.textContent = `${member.user.displayName} (@${member.user.username})`; choices.append(option); });
    select("#server-settings-dialog").showModal();
  }
  async function showMembersData() { const result = await api(`/api/v1/servers/${encodeURIComponent(selected.id)}/members`, {headers: {}}); members = result.members || []; }

  select("#add-server-button").addEventListener("click", () => select("#server-create-dialog").showModal());
  select("#server-create-invites").addEventListener("click", () => { select("#server-create-dialog").close(); showInvitations(); });
  select("#server-invites-destination").addEventListener("click", showInvitations);
  select("#server-general-button").addEventListener("click", () => selected && open(selected.id));
  select("#server-members-button").addEventListener("click", () => showMembers().catch((error) => notify(error.message, "error")));
  select("#server-invite-button").addEventListener("click", showInvite); select("#server-invite-search").addEventListener("input", showInvite);
  select("#server-settings-button").addEventListener("click", () => showSettings().catch((error) => notify(error.message, "error")));
  document.querySelectorAll("[data-close-dialog]").forEach((button) => button.addEventListener("click", () => select(`#${button.dataset.closeDialog}`).close()));
  select("#server-create-form").addEventListener("submit", async (event) => { event.preventDefault(); try { const result = await api("/api/v1/servers", {method: "POST", body: JSON.stringify({name: event.currentTarget.elements.name.value})}); event.currentTarget.reset(); select("#server-create-dialog").close(); await load(); await open(result.server.id); } catch (error) { notify(error.message, "error"); } });
  select("#server-rename-form").addEventListener("submit", async (event) => { event.preventDefault(); try { await api(`/api/v1/servers/${encodeURIComponent(selected.id)}`, {method: "PATCH", body: JSON.stringify({name: event.currentTarget.elements.name.value})}); await load(); await open(selected.id); select("#server-settings-dialog").close(); } catch (error) { notify(error.message, "error"); } });
  select("#server-transfer-form").addEventListener("submit", async (event) => { event.preventDefault(); try { await api(`/api/v1/servers/${encodeURIComponent(selected.id)}/ownership`, {method: "POST", body: JSON.stringify({userId: event.currentTarget.elements.userId.value, confirm: event.currentTarget.elements.confirm.checked})}); await load(); select("#server-settings-dialog").close(); } catch (error) { notify(error.message, "error"); } });
  select("#server-leave-button").addEventListener("click", async () => { try { await api(`/api/v1/servers/${encodeURIComponent(selected.id)}/leave`, {method: "POST", body: "{}"}); select("#server-settings-dialog").close(); selected = null; await load(); navigation.destination("home"); navigation.friends(); } catch (error) { notify(error.message, "error"); } });
  select("#server-delete-form").addEventListener("submit", async (event) => { event.preventDefault(); try { await api(`/api/v1/servers/${encodeURIComponent(selected.id)}`, {method: "DELETE", body: JSON.stringify({name: event.currentTarget.elements.name.value})}); select("#server-settings-dialog").close(); selected = null; await load(); navigation.destination("home"); navigation.friends(); } catch (error) { notify(error.message, "error"); } });

  return {load, open, showInvitations, invitationCount: () => invites.length, getServers: () => servers, selected: () => selected};
}
