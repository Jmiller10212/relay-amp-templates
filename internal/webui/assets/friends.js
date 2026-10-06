import {api} from "/api.js?v=0.7.0";

export function createFriendsUI(select, notify, openDirect) {
  let friends = [];
  let requests = {incoming: [], outgoing: []};
  let activeTab = "online";
  let pendingRemove = null;

  const tabViews = {
    online: select("#friends-online-view"),
    all: select("#friends-all-view"),
    pending: select("#friends-pending-view"),
    add: select("#friends-add-view"),
  };

  function avatar(user, online) {
    const item = document.createElement("div");
    item.className = "friend-avatar";
    item.textContent = (user.displayName || user.username).slice(0, 1).toUpperCase();
    const dot = document.createElement("i");
    dot.className = `presence-dot ${online ? "online" : ""}`;
    item.append(dot);
    return item;
  }

  function identity(user, subtitle) {
    const copy = document.createElement("div");
    copy.className = "friend-copy";
    const display = document.createElement("strong");
    const detail = document.createElement("small");
    display.textContent = user.displayName;
    detail.textContent = subtitle || `@${user.username}`;
    copy.append(display, detail);
    return copy;
  }

  function action(label, className, handler) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `action-button ${className || ""}`;
    button.textContent = label;
    button.setAttribute("aria-label", className || label);
    button.addEventListener("click", async (event) => {
      event.stopPropagation();
      try { await handler(); } catch (error) { notify(error.message, "error"); }
    });
    return button;
  }

  function friendRow(friend) {
    const row = document.createElement("div");
    row.className = "friend-row clickable";
    row.append(avatar(friend, friend.online), identity(friend, `@${friend.username} · ${friend.online ? "Online" : "Offline"}`));
    const actions = document.createElement("div");
    actions.className = "row-actions";
    actions.append(action("●", "Message friend", () => openDirect(friend.id)), action("⋯", "More options", () => confirmRemove(friend)));
    row.append(actions);
    row.addEventListener("click", () => openDirect(friend.id));
    return row;
  }

  function requestRow(request, incoming) {
    const user = incoming ? request.requester : request.recipient;
    const row = document.createElement("div");
    row.className = "friend-row";
    row.append(avatar(user, false), identity(user, `@${user.username} · ${incoming ? "Incoming request" : "Outgoing request"}`));
    const actions = document.createElement("div");
    actions.className = "row-actions";
    if (incoming) {
      actions.append(action("✓", "accept", () => mutate(`/api/v1/friend-requests/${request.id}/accept`, "POST")), action("×", "decline", () => mutate(`/api/v1/friend-requests/${request.id}/decline`, "POST")));
    } else {
      actions.append(action("×", "cancel", () => mutate(`/api/v1/friend-requests/${request.id}`, "DELETE")));
    }
    row.append(actions);
    return row;
  }

  function empty(text) {
    const item = document.createElement("p");
    item.className = "empty";
    item.textContent = text;
    return item;
  }

  function matches(friend, value) {
    const query = value.trim().toLocaleLowerCase();
    return !query || friend.displayName.toLocaleLowerCase().includes(query) || friend.username.toLocaleLowerCase().includes(query);
  }

  function render() {
    const onlineFilter = select("#online-filter").value;
    const allFilter = select("#all-filter").value;
    const online = friends.filter((friend) => friend.online && matches(friend, onlineFilter));
    const all = friends.filter((friend) => matches(friend, allFilter));
    const onlineList = select("#friends-online");
    const allList = select("#friends-all");
    const incomingList = select("#friends-incoming");
    const outgoingList = select("#friends-outgoing");
    onlineList.textContent = "";
    allList.textContent = "";
    incomingList.textContent = "";
    outgoingList.textContent = "";
    online.forEach((friend) => onlineList.append(friendRow(friend)));
    all.forEach((friend) => allList.append(friendRow(friend)));
    requests.incoming.forEach((request) => incomingList.append(requestRow(request, true)));
    requests.outgoing.forEach((request) => outgoingList.append(requestRow(request, false)));
    if (!online.length) onlineList.append(empty("No friends are online."));
    if (!all.length) allList.append(empty(friends.length ? "No friends match that search." : "No friends yet."));
    if (!requests.incoming.length) incomingList.append(empty("No incoming requests."));
    if (!requests.outgoing.length) outgoingList.append(empty("No outgoing requests."));
    select("#online-friends-count").textContent = String(online.length);
    select("#all-friends-count").textContent = String(all.length);
    select("#incoming-count").textContent = String(requests.incoming.length);
    select("#outgoing-count").textContent = String(requests.outgoing.length);
    [select("#friends-badge"), select("#pending-tab-badge")].forEach((badge) => {
      badge.textContent = String(requests.incoming.length);
      badge.hidden = requests.incoming.length === 0;
    });
  }

  async function load() {
    const [friendResult, requestResult] = await Promise.all([
      api("/api/v1/friends", {headers: {}}),
      api("/api/v1/friend-requests", {headers: {}}),
    ]);
    friends = friendResult.friends || [];
    requests = {incoming: requestResult.incoming || [], outgoing: requestResult.outgoing || []};
    render();
    return {friends, requests};
  }

  async function mutate(path, method) {
    await api(path, {method, ...(method === "POST" ? {body: "{}"} : {})});
    await load();
  }

  function setTab(tab) {
    activeTab = tab;
    Object.entries(tabViews).forEach(([name, view]) => { view.hidden = name !== tab; });
    document.querySelectorAll("[data-friends-tab]").forEach((button) => button.classList.toggle("active", button.dataset.friendsTab === tab));
  }

  function confirmRemove(friend) {
    pendingRemove = friend;
    select("#remove-friend-copy").textContent = `Remove ${friend.displayName} (@${friend.username})? Your existing direct-message history will remain read-only.`;
    select("#remove-friend-dialog").showModal();
  }

  document.querySelectorAll("[data-friends-tab]").forEach((button) => button.addEventListener("click", () => setTab(button.dataset.friendsTab)));
  select("#online-filter").addEventListener("input", render);
  select("#all-filter").addEventListener("input", render);
  select("#add-friend-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const input = select("#friend-username");
    const status = select("#add-friend-status");
    status.className = "inline-status";
    try {
      const result = await api("/api/v1/friend-requests", {method: "POST", body: JSON.stringify({username: input.value})});
      input.value = "";
      status.textContent = result.status === "friends" ? "You are now friends." : "Friend request sent.";
      status.classList.add("success");
      await load();
    } catch (error) {
      status.textContent = error.message;
      status.classList.add("error");
    }
  });
  select("#remove-friend-close").addEventListener("click", () => select("#remove-friend-dialog").close());
  select("#remove-friend-cancel").addEventListener("click", () => select("#remove-friend-dialog").close());
  select("#remove-friend-confirm").addEventListener("click", async () => {
    if (!pendingRemove) return;
    try {
      await api(`/api/v1/friends/${encodeURIComponent(pendingRemove.id)}`, {method: "DELETE"});
      select("#remove-friend-dialog").close();
      pendingRemove = null;
      await load();
    } catch (error) { notify(error.message, "error"); }
  });

  setTab(activeTab);
  return {load, render, setTab, getFriends: () => friends, getRequests: () => requests};
}
