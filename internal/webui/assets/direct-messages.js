import {api} from "/api.js?v=0.7.1";

export function createDirectMessagesUI(select, friendsUI, notify, activateConversation, getActiveConversation) {
  let conversations = [];
  let selectedFriendID = "";

  function avatar(user, online) {
    const item = document.createElement("span");
    item.className = "dm-avatar";
    item.textContent = (user.displayName || user.username).slice(0, 1).toUpperCase();
    if (online !== undefined) {
      const dot = document.createElement("i");
      dot.className = `presence-dot ${online ? "online" : ""}`;
      item.append(dot);
    }
    return item;
  }

  function copy(user, subtitle) {
    const item = document.createElement("span");
    item.className = "dm-copy";
    const display = document.createElement("strong");
    const detail = document.createElement("small");
    display.textContent = user.displayName;
    detail.textContent = subtitle;
    item.append(display, detail);
    return item;
  }

  function conversationButton(conversation) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = `dm-item ${getActiveConversation()?.id === conversation.id ? "active" : ""}`;
    const subtitle = conversation.canSend ? `@${conversation.peer.username}` : "Read only";
    button.append(avatar(conversation.peer, conversation.online), copy(conversation.peer, subtitle));
    if (conversation.unreadCount > 0) {
      const unread = document.createElement("b");
      unread.className = "unread-badge";
      unread.textContent = conversation.unreadCount > 99 ? "99+" : String(conversation.unreadCount);
      button.append(unread);
    } else {
      button.append(document.createElement("span"));
    }
    button.addEventListener("click", () => activateConversation(conversation));
    return button;
  }

  function friendButton(friend) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "dm-item";
    button.append(avatar(friend, friend.online), copy(friend, `@${friend.username}`), document.createElement("span"));
    button.addEventListener("click", () => openForUser(friend.id));
    return button;
  }

  function matches(user, query) {
    const value = query.trim().toLocaleLowerCase();
    return !value || user.displayName.toLocaleLowerCase().includes(value) || user.username.toLocaleLowerCase().includes(value);
  }

  function render() {
    const list = select("#dm-list");
    const query = select("#home-search").value;
    list.textContent = "";
    const shown = conversations.filter((conversation) => matches(conversation.peer, query));
    shown.forEach((conversation) => list.append(conversationButton(conversation)));
    if (query.trim()) {
      const existingPeers = new Set(conversations.map((conversation) => conversation.peer.id));
      friendsUI.getFriends().filter((friend) => !existingPeers.has(friend.id) && matches(friend, query)).forEach((friend) => list.append(friendButton(friend)));
    }
    if (!list.children.length) {
      const empty = document.createElement("p");
      empty.className = "empty";
      empty.textContent = query.trim() ? "No friends or conversations match." : "No direct messages yet.";
      list.append(empty);
    }
  }

  async function load() {
    const result = await api("/api/v1/direct-conversations", {headers: {}});
    conversations = result.conversations || [];
    render();
    return conversations;
  }

  async function openForUser(userID) {
    try {
      const result = await api("/api/v1/direct-conversations", {method: "POST", body: JSON.stringify({userId: userID})});
      await load();
      const conversation = conversations.find((item) => item.id === result.conversation.id) || result.conversation;
      activateConversation(conversation);
      closeModal();
      return conversation;
    } catch (error) {
      notify(error.message, "error");
      throw error;
    }
  }

  function renderModal() {
    const query = select("#new-dm-search").value;
    const list = select("#new-dm-list");
    list.textContent = "";
    const friends = friendsUI.getFriends().filter((friend) => matches(friend, query));
    friends.forEach((friend) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = `modal-friend ${selectedFriendID === friend.id ? "selected" : ""}`;
      button.setAttribute("role", "option");
      button.setAttribute("aria-selected", String(selectedFriendID === friend.id));
      const selection = document.createElement("span");
      selection.className = "selection-dot";
      button.append(avatar(friend, friend.online), copy(friend, `@${friend.username}`), selection);
      button.addEventListener("click", () => { selectedFriendID = friend.id; renderModal(); });
      button.addEventListener("dblclick", () => openForUser(friend.id));
      list.append(button);
    });
    if (!friends.length) {
      const empty = document.createElement("p");
      empty.className = "empty";
      empty.textContent = "No friends match that search.";
      list.append(empty);
    }
    select("#new-dm-open").disabled = !selectedFriendID;
  }

  function openModal() {
    selectedFriendID = "";
    select("#new-dm-search").value = "";
    renderModal();
    select("#new-dm-dialog").showModal();
    select("#new-dm-search").focus();
  }
  function closeModal() {
    if (select("#new-dm-dialog").open) select("#new-dm-dialog").close();
  }

  select("#home-search").addEventListener("input", render);
  select("#new-dm-button").addEventListener("click", openModal);
  select("#new-dm-close").addEventListener("click", closeModal);
  select("#new-dm-cancel").addEventListener("click", closeModal);
  select("#new-dm-search").addEventListener("input", renderModal);
  select("#new-dm-open").addEventListener("click", () => { if (selectedFriendID) openForUser(selectedFriendID); });

  return {load, render, openForUser, getConversations: () => conversations, unreadMessages: () => conversations.reduce((sum, item) => sum + item.unreadCount, 0)};
}
