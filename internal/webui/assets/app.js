import {api} from "/api.js?v=0.7.4";
import {state} from "/state.js?v=0.7.4";
import {RelayRealtime} from "/realtime.js?v=0.7.4";
import {addMessage, prependMessages, newestMessageId, renderUsers} from "/conversation.js?v=0.7.4";
import {formPayload, normalizeUsername, setFormBusy, setFormError} from "/auth.js?v=0.7.4";
import {renderAccount} from "/settings.js?v=0.7.4";
import {createFriendsUI} from "/friends.js?v=0.7.4";
import {createDirectMessagesUI} from "/direct-messages.js?v=0.7.4";
import {createNavigation} from "/navigation.js?v=0.7.4";
import {createServersUI} from "/servers.js?v=0.7.4";

const $ = (selector) => document.querySelector(selector);
const authShell = $("#auth-shell");
const chatShell = $("#chat-shell");
const authNotice = $("#notice");
const appNotice = $("#app-notice");
const messages = $("#messages");
const loadEarlier = $("#load-earlier");
const userList = $("#user-list");
const statusPill = $("#status-pill");
const navigation = createNavigation($);
let lastRegistrationEmail = "";
let lastReadSent = new Map();
let dmUI;
let serverUI;
let restoreTimer = null;
let restoreDelay = 1000;
let globalPresenceUsers = [];
let mentionMatches = [];
let mentionIndex = 0;
const friendsUI = createFriendsUI($, showNotice, (userID) => dmUI.openForUser(userID));
dmUI = createDirectMessagesUI($, friendsUI, showNotice, activateConversation, () => state.activeConversation);
serverUI = createServersUI($, friendsUI, showNotice, navigation, activateConversation);

function showNotice(text, kind = "info") {
  const target = authShell.hidden ? appNotice : authNotice;
  target.onclick = null; target.onkeydown = null; target.removeAttribute("role"); target.removeAttribute("tabindex");
  target.textContent = text;
  target.className = `notice ${target === appNotice ? "app-notice " : ""}${kind}`;
  target.hidden = !text;
  if (target === appNotice && text) setTimeout(() => { if (target.textContent === text) target.hidden = true; }, 5000);
}

function playNotificationSound() {
  const audio = $("#notification-audio");
  if (!audio) return;
  audio.currentTime = 0;
  audio.play().catch(() => {});
}

function showClickableNotice(text, open) {
  appNotice.textContent = text; appNotice.className = "notice app-notice info clickable"; appNotice.hidden = false;
  appNotice.setAttribute("role", "button"); appNotice.tabIndex = 0;
  appNotice.onclick = open; appNotice.onkeydown = (event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); open(); } };
  setTimeout(() => { if (appNotice.textContent === text) appNotice.hidden = true; }, 8000);
}

function unlockNotificationSound() {
  const audio = $("#notification-audio"); if (!audio) return;
  document.removeEventListener("pointerdown", unlockNotificationSound);
  document.removeEventListener("keydown", unlockNotificationSound);
  const volume = audio.volume; audio.volume = 0;
  audio.play().then(() => { audio.pause(); audio.currentTime = 0; audio.volume = volume; }).catch(() => { audio.volume = volume; });
}
document.addEventListener("pointerdown", unlockNotificationSound);
document.addEventListener("keydown", unlockNotificationSound);
function showView(id) { document.querySelectorAll(".view").forEach((view) => { view.hidden = view.id !== id; }); showNotice(""); }
function setConnection(label, online) { statusPill.textContent = label; statusPill.className = `pill ${online ? "online" : "offline"}`; $("#connection-dot").className = online ? "online" : ""; }

document.querySelectorAll('input[name="username"]').forEach((input) => input.addEventListener("input", () => { input.value = input.value.normalize("NFKC").toLowerCase().replace(/[^a-z0-9_]/g, ""); }));
document.addEventListener("click", (event) => { const target = event.target.closest("[data-view]"); if (target) showView(target.dataset.view); });

async function submit(form, path, after, method = "POST") {
  setFormError(form, null); setFormBusy(form, true);
  try { await after(await api(path, {method, body: JSON.stringify(formPayload(form))})); }
  catch (error) { setFormError(form, error); }
  finally { setFormBusy(form, false); }
}

$("#login-view").addEventListener("submit", (event) => { event.preventDefault(); submit(event.currentTarget, "/api/auth/login", enterAccount); });
$("#register-view").addEventListener("submit", (event) => { event.preventDefault(); normalizeUsername(event.currentTarget); lastRegistrationEmail = formPayload(event.currentTarget).email; submit(event.currentTarget, "/api/auth/register", async () => showView("check-email-view")); });
$("#forgot-view").addEventListener("submit", (event) => { event.preventDefault(); submit(event.currentTarget, "/api/auth/forgot-password", async () => { showView("check-email-view"); showNotice("If that address is registered, a recovery email is on its way.", "success"); }); });
$("#finish-view").addEventListener("submit", (event) => { event.preventDefault(); normalizeUsername(event.currentTarget); submit(event.currentTarget, "/api/account/complete-profile", enterAccount); });
$("#reset-view").addEventListener("submit", (event) => { event.preventDefault(); submit(event.currentTarget, "/api/auth/reset-password", async () => { showView("login-view"); showNotice("Password updated. You can now sign in.", "success"); }); });
$("#resend-button").addEventListener("click", async () => { if (!lastRegistrationEmail) { showView("login-view"); return; } try { await api("/api/auth/resend-verification", {method: "POST", body: JSON.stringify({email: lastRegistrationEmail})}); showNotice("If that address is eligible, another email is on its way.", "success"); } catch (error) { showNotice(error.message, "error"); } });

async function loadAccount() {
  clearTimeout(restoreTimer);
  if (!state.account) { authShell.hidden = false; chatShell.hidden = true; showView("restoring-view"); }
  try {
    const result = await api("/api/v1/me", {headers: {}});
    if (result.profileRequired) { state.account = result; showView("finish-view"); return; }
    await enterAccount(result);
    restoreDelay = 1000;
  } catch (error) {
    if (error.status === 401) { state.account = null; authShell.hidden = false; chatShell.hidden = true; showView("login-view"); return; }
    if (!state.account) showView("restoring-view");
    else { authShell.hidden = true; chatShell.hidden = false; setConnection("Restoring session", false); }
    restoreTimer = setTimeout(loadAccount, restoreDelay);
    restoreDelay = Math.min(restoreDelay * 2, 15000);
  }
}

async function enterAccount(result) {
  state.account = result;
  if (!result.profile) { authShell.hidden = false; chatShell.hidden = true; showView("finish-view"); return; }
  authShell.hidden = true; chatShell.hidden = false; renderAccount(result, $);
  state.bootstrap = await api("/api/v1/bootstrap", {headers: {}});
  $("#room-name").textContent = `# ${state.bootstrap.globalLobby.name}`;
  await friendsUI.load();
  if (state.bootstrap.capabilities.directMessages) await dmUI.load();
  else $("#new-dm-button").disabled = true;
  await refreshPresence();
  if (state.bootstrap.capabilities.servers) await serverUI.load(state.bootstrap);
  showHomeFriends();
  updateBadges();
  connect();
}

function showHomeFriends() {
  state.destination = "home";
  state.activeConversation = null;
  navigation.destination("home");
  serverUI.deactivateChannel();
  navigation.friends();
  friendsUI.render();
  dmUI.render();
}

async function showLobby() {
  state.destination = "lobby";
  navigation.destination("lobby");
  serverUI.deactivateChannel();
  await activateConversation({id: state.bootstrap.globalLobby.id, kind: "global", name: state.bootstrap.globalLobby.name, canSend: true});
}

async function activateConversation(conversation, options = {}) {
  state.activeConversation = conversation;
  const channelConversation = conversation.kind === "channel";
  $("#server-channel-tools").hidden = !channelConversation;
  $("#conversation-view").classList.toggle("can-pin", channelConversation && serverUI.selected()?.role === "owner");
  if (!channelConversation) serverUI.deactivateChannel();
  if (conversation.kind === "direct") {
    state.destination = "home";
    navigation.destination("home");
    $("#conversation-title").textContent = conversation.peer.displayName;
    $("#conversation-subtitle").textContent = `@${conversation.peer.username}`;
    $("#message-input").placeholder = `Message @${conversation.peer.username}`;
    const presence = $("#conversation-presence");
    presence.hidden = conversation.online === undefined;
    if (!presence.hidden) { presence.textContent = conversation.online ? "Online" : "Offline"; presence.className = `pill ${conversation.online ? "online" : "offline"}`; }
    $("#read-only-banner").hidden = conversation.canSend;
    setComposerEnabled(conversation.canSend);
    navigation.conversation(conversation.peer.displayName);
  } else {
    if (conversation.kind === "channel") state.destination = "server";
    $("#conversation-title").textContent = `# ${conversation.name}`;
    $("#conversation-subtitle").textContent = conversation.kind === "channel" ? conversation.serverName : "The shared Relay room";
    $("#message-input").placeholder = `Message #${conversation.name}`;
    $("#conversation-presence").hidden = true;
    $("#read-only-banner").hidden = true;
    setComposerEnabled(true);
    navigation.conversation(`# ${conversation.name}`);
  }
  dmUI.render();
  closeMentionSuggestions();
  if (conversation.kind === "channel" && document.visibilityState === "visible") serverUI.markConversationSeen(conversation.id);
  if (options.targetMessageId) await loadMessageContext(options.targetMessageId);
  else await loadHistory();
}

function setComposerEnabled(enabled) {
  $("#message-input").disabled = !enabled;
  $("#message-form button").disabled = !enabled;
}

async function loadHistory() {
  if (!state.activeConversation) return;
  state.messageIds.clear();
  messages.replaceChildren(loadEarlier);
  const result = await api(`/api/v1/conversations/${encodeURIComponent(state.activeConversation.id)}/messages?limit=50`, {headers: {}});
  result.messages.forEach((message) => addMessage(messages, state.messageIds, message, {scroll: false}));
  state.history = {hasMore: Boolean(result.hasMore), nextBefore: result.nextBefore || 0, loading: false};
  loadEarlier.hidden = !state.history.hasMore;
  messages.scrollTop = messages.scrollHeight;
  syncPinButtons();
  await markReadIfVisible();
}

async function loadMessageContext(messageID) {
  if (!state.activeConversation) return;
  state.messageIds.clear(); messages.replaceChildren(loadEarlier); loadEarlier.hidden = true;
  const result = await api(`/api/v1/conversations/${encodeURIComponent(state.activeConversation.id)}/messages/${encodeURIComponent(messageID)}/context`, {headers: {}});
  (result.messages || []).forEach((message) => addMessage(messages, state.messageIds, message, {scroll: false}));
  state.history = {hasMore: false, nextBefore: 0, loading: false}; syncPinButtons();
  const target = messages.querySelector(`[data-message-id="${CSS.escape(String(messageID))}"]`);
  if (target) { target.classList.add("target-message"); target.scrollIntoView({block: "center"}); setTimeout(() => target.classList.remove("target-message"), 3300); }
}

function syncPinButtons() {
  messages.querySelectorAll("[data-pin-message]").forEach((button) => {
    const pinned = serverUI.isPinned(Number(button.dataset.pinMessage));
    button.classList.toggle("pinned", pinned); button.title = pinned ? "Unpin message" : "Pin message"; button.setAttribute("aria-label", button.title);
  });
}

async function loadOlderHistory() {
  if (!state.activeConversation || !state.history.hasMore || state.history.loading) return;
  state.history.loading = true;
  loadEarlier.disabled = true;
  try {
    const result = await api(`/api/v1/conversations/${encodeURIComponent(state.activeConversation.id)}/messages?limit=50&before=${state.history.nextBefore}`, {headers: {}});
    prependMessages(messages, state.messageIds, result.messages, loadEarlier.nextSibling);
    syncPinButtons();
    state.history.hasMore = Boolean(result.hasMore);
    state.history.nextBefore = result.nextBefore || 0;
    loadEarlier.hidden = !state.history.hasMore;
  } catch (error) { showNotice(error.message, "error"); }
  finally { state.history.loading = false; loadEarlier.disabled = false; }
}

async function refreshPresence() {
  try { const result = await api("/api/v1/presence", {headers: {}}); globalPresenceUsers = result.users || []; renderUsers(userList, $("#online-count"), globalPresenceUsers); } catch (_) {}
}

function connect() {
  if (!state.account?.profile || state.socket) return;
  setConnection("Connecting", false);
  state.socket = new RelayRealtime(handleRealtime, async (status) => {
    if (status === "open") { state.reconnectDelay = 1000; setConnection("Connected", true); return; }
    state.socket = null; setConnection("Reconnecting", false);
    if (state.closing) return;
    try {
      const me = await api("/api/v1/me", {headers: {}});
      if (me.profileRequired) { leaveChat(); showView("finish-view"); return; }
      state.account = me; renderAccount(me, $);
      state.reconnectTimer = setTimeout(connect, state.reconnectDelay);
      state.reconnectDelay = Math.min(state.reconnectDelay * 2, 15000);
    } catch (error) {
      if (error.status === 401) { leaveChat(); showView("login-view"); showNotice("Your session ended. Please sign in again.", "error"); return; }
      setConnection("Restoring session", false);
      state.reconnectTimer = setTimeout(connect, state.reconnectDelay);
      state.reconnectDelay = Math.min(state.reconnectDelay * 2, 15000);
    }
  });
  state.socket.connect();
}

async function handleRealtime(event) {
  if (event.type === "message.created" && event.data?.message) {
    const message = event.data.message;
    const fromAnotherUser = message.userId !== state.account?.profile?.id;
    const inactiveOrHidden = message.conversationId !== state.activeConversation?.id || document.visibilityState !== "visible";
    if (message.conversationId === state.activeConversation?.id) {
      const nearBottom = messages.scrollHeight - messages.scrollTop - messages.clientHeight < 80;
      addMessage(messages, state.messageIds, message, {scroll: nearBottom});
      syncPinButtons();
      if (nearBottom) await markReadIfVisible();
    }
    if (event.data?.serverId && fromAnotherUser && inactiveOrHidden) await handleChannelActivity(message);
    if (!event.data?.serverId && message.conversationId !== state.bootstrap?.globalLobby?.id) {
      await refreshDirectState();
      if (fromAnotherUser && inactiveOrHidden) playDirectMessageSound(message);
    }
  } else if (event.type === "presence.changed" || event.type === "profile.updated") {
    await refreshPresence(); await refreshSocialState(); await serverUI.handleRealtime(event);
  } else if (event.type.startsWith("friend.") || event.type.startsWith("friendship.")) {
    await refreshSocialState();
  } else if (event.type === "conversation.created" || event.type === "conversation.unread_updated") {
    await refreshDirectState();
  } else if (event.type.startsWith("server.") || event.type === "channel.created") {
    await serverUI.handleRealtime(event);
    await refreshServerState();
    if (event.type === "server.invite.created") showServerInvitationAlert(event.data?.invite);
  } else if (event.type === "channel.pin.created" || event.type === "channel.pin.removed") {
    await serverUI.handleRealtime(event); syncPinButtons();
  } else if (event.type === "error") showNotice(event.data?.message || "Realtime error", "error");
}

function isCurrentUserMentioned(text) {
  const username = state.account?.profile?.username; if (!username) return false;
  const escaped = username.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(^|[^a-z0-9_])@${escaped}(?![a-z0-9_])`, "i").test(text);
}

async function handleChannelActivity(message) {
  if (!serverUI.channelForConversation(message.conversationId)) return;
  let mode = "mentions";
  try { mode = await serverUI.notificationModeForConversation(message.conversationId); } catch (_) { return; }
  const mentioned = isCurrentUserMentioned(message.text);
  serverUI.recordChannelActivity(message.conversationId, mentioned, mode);
  if (mode === "nothing" || !mentioned) return;
  playNotificationSound();
}

function playDirectMessageSound(message) {
  const conversation = dmUI.getConversations().find((item) => item.id === message.conversationId);
  if (!conversation) return;
  playNotificationSound();
}

function showServerInvitationAlert(invite) {
  const name = invite?.server?.name || "a server";
  showClickableNotice(`You were invited to ${name}.`, () => serverUI.showInvitations()); playNotificationSound();
}

async function refreshSocialState() {
  await friendsUI.load();
  if (state.bootstrap?.capabilities?.directMessages) await dmUI.load();
  reconcileActiveDirect();
  updateBadges();
}

async function refreshDirectState() {
  if (!state.bootstrap?.capabilities?.directMessages) return;
  await dmUI.load();
  reconcileActiveDirect();
  updateBadges();
}

async function refreshServerState() {
  if (!state.bootstrap?.capabilities?.servers) return;
  const selectedID = serverUI.selected()?.id;
  await serverUI.load();
  if (selectedID && serverUI.getServers().some((item) => item.id === selectedID)) await serverUI.open(selectedID);
  else if (state.destination === "server") showHomeFriends();
  updateBadges();
}

function reconcileActiveDirect() {
  if (state.activeConversation?.kind !== "direct") return;
  const current = dmUI.getConversations().find((item) => item.id === state.activeConversation.id);
  if (!current) return;
  state.activeConversation = current;
  $("#read-only-banner").hidden = current.canSend;
  setComposerEnabled(current.canSend);
  const presence = $("#conversation-presence");
  presence.hidden = current.online === undefined;
  if (!presence.hidden) { presence.textContent = current.online ? "Online" : "Offline"; presence.className = `pill ${current.online ? "online" : "offline"}`; }
}

function updateBadges() {
  const incoming = friendsUI.getRequests().incoming.length;
  navigation.updateHomeBadge(incoming + dmUI.unreadMessages() + serverUI.invitationCount());
}

async function markReadIfVisible() {
  const active = state.activeConversation;
  if (!active || active.kind !== "direct" || document.visibilityState !== "visible") return;
  const nearBottom = messages.scrollHeight - messages.scrollTop - messages.clientHeight < 80;
  if (!nearBottom) return;
  const messageID = newestMessageId(messages);
  if (!messageID || (lastReadSent.get(active.id) || 0) >= messageID) return;
  lastReadSent.set(active.id, messageID);
  try {
    await api(`/api/v1/conversations/${encodeURIComponent(active.id)}/read`, {method: "PUT", body: JSON.stringify({messageId: messageID})});
    await refreshDirectState();
  } catch (error) {
    lastReadSent.delete(active.id);
    if (error.code !== "conversation_not_found") showNotice(error.message, "error");
  }
}

function mentionCandidates() {
  if (!state.activeConversation) return [];
  let candidates = [];
  if (state.activeConversation.kind === "channel") candidates = serverUI.mentionCandidates();
  else if (state.activeConversation.kind === "direct") candidates = [state.activeConversation.peer];
  else candidates = globalPresenceUsers;
  const ownID = state.account?.profile?.id, seen = new Set();
  return candidates.filter((user) => user?.id && user.id !== ownID && !seen.has(user.id) && seen.add(user.id));
}

function closeMentionSuggestions() {
  mentionMatches = []; mentionIndex = 0;
  const list = $("#mention-suggestions"), input = $("#message-input");
  if (list) { list.hidden = true; list.textContent = ""; }
  if (input) input.setAttribute("aria-expanded", "false");
}

function renderMentionSuggestions() {
  const input = $("#message-input"), list = $("#mention-suggestions");
  const caret = input.selectionStart ?? input.value.length;
  const match = input.value.slice(0, caret).match(/(^|\s)@([a-z0-9_]*)$/i);
  if (!match) { closeMentionSuggestions(); return; }
  const query = match[2].toLowerCase(), start = caret - query.length - 1;
  mentionMatches = mentionCandidates().filter((user) => user.username.toLowerCase().includes(query) || user.displayName.toLowerCase().includes(query)).slice(0, 8).map((user) => ({user, start, end: caret}));
  if (!mentionMatches.length) { closeMentionSuggestions(); return; }
  mentionIndex = Math.min(mentionIndex, mentionMatches.length - 1); list.textContent = "";
  mentionMatches.forEach((item, index) => {
    const button = document.createElement("button"); button.type = "button"; button.setAttribute("role", "option"); button.setAttribute("aria-selected", String(index === mentionIndex));
    const name = document.createElement("strong"); name.textContent = item.user.displayName;
    const handle = document.createElement("small"); handle.textContent = `@${item.user.username}`;
    button.append(name, handle); button.addEventListener("mousedown", (event) => event.preventDefault()); button.addEventListener("click", () => selectMention(index)); list.append(button);
  });
  list.hidden = false; input.setAttribute("aria-expanded", "true");
}

function selectMention(index = mentionIndex) {
  const item = mentionMatches[index], input = $("#message-input"); if (!item) return;
  const replacement = `@${item.user.username} `;
  input.value = input.value.slice(0, item.start) + replacement + input.value.slice(item.end);
  const caret = item.start + replacement.length; input.setSelectionRange(caret, caret); closeMentionSuggestions(); input.focus();
}

function moveMentionSelection(delta) {
  if (!mentionMatches.length) return;
  mentionIndex = (mentionIndex + delta + mentionMatches.length) % mentionMatches.length;
  [...$("#mention-suggestions").children].forEach((button, index) => button.setAttribute("aria-selected", String(index === mentionIndex)));
}

$("#message-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!state.activeConversation) return;
  const input = $("#message-input");
  const text = input.value.trim();
  if (!text) return;
  input.disabled = true;
  try {
    const result = await api(`/api/v1/conversations/${encodeURIComponent(state.activeConversation.id)}/messages`, {method: "POST", body: JSON.stringify({text})});
    addMessage(messages, state.messageIds, result.message); syncPinButtons(); input.value = "";
    if (state.activeConversation.kind === "direct") await refreshDirectState();
  } catch (error) { showNotice(error.message, "error"); }
  finally { input.disabled = !state.activeConversation?.canSend; input.focus(); }
});

$("#message-input").addEventListener("input", () => { mentionIndex = 0; renderMentionSuggestions(); });
$("#message-input").addEventListener("keydown", (event) => {
  if (!mentionMatches.length) return;
  if (event.key === "ArrowDown" || event.key === "ArrowUp") { event.preventDefault(); moveMentionSelection(event.key === "ArrowDown" ? 1 : -1); }
  else if (event.key === "Tab" || event.key === "Enter") { event.preventDefault(); selectMention(); }
  else if (event.key === "Escape") { event.preventDefault(); closeMentionSuggestions(); }
});
$("#message-input").addEventListener("blur", () => setTimeout(closeMentionSuggestions, 120));

loadEarlier.addEventListener("click", loadOlderHistory);
messages.addEventListener("click", async (event) => {
  const button = event.target.closest("[data-pin-message]"); if (!button) return;
  try { await serverUI.togglePin(Number(button.dataset.pinMessage)); syncPinButtons(); } catch (error) { showNotice(error.message, "error"); }
});
messages.addEventListener("scroll", () => { if (messages.scrollTop < 32 && state.history.hasMore) loadOlderHistory(); markReadIfVisible(); });
document.addEventListener("visibilitychange", () => {
  markReadIfVisible();
  if (document.visibilityState === "visible" && state.activeConversation?.kind === "channel") serverUI.markConversationSeen(state.activeConversation.id);
});
$("#rail-home").addEventListener("click", () => { if (state.destination === "home" && state.activeConversation?.kind === "direct") activateConversation(state.activeConversation); else showHomeFriends(); });
$("#rail-lobby").addEventListener("click", showLobby);
$("#lobby-room-button").addEventListener("click", showLobby);
$("#friends-destination").addEventListener("click", showHomeFriends);

const dialog = $("#settings-dialog");
$("#account-button").addEventListener("click", () => dialog.showModal());
$("#settings-close").addEventListener("click", () => dialog.close());
$("#display-form").addEventListener("submit", (event) => { event.preventDefault(); submit(event.currentTarget, "/api/v1/me/display-name", async (result) => { state.account = result; renderAccount(result, $); showNotice("Display name updated.", "success"); }, "PATCH"); });
$("#username-form").addEventListener("submit", (event) => { event.preventDefault(); normalizeUsername(event.currentTarget); submit(event.currentTarget, "/api/v1/me/username", async (result) => { state.account = result; renderAccount(result, $); event.currentTarget.elements.password.value = ""; showNotice("Username updated.", "success"); }); });
$("#password-form").addEventListener("submit", (event) => { event.preventDefault(); submit(event.currentTarget, "/api/v1/me/password", async () => { event.currentTarget.reset(); showNotice("Password updated and other sessions were signed out.", "success"); }); });
$("#logout-button").addEventListener("click", async () => { try { await api("/api/auth/logout", {method: "POST", body: "{}"}); } finally { state.closing = true; state.socket?.close(); dialog.close(); leaveChat(); showView("login-view"); state.closing = false; } });

function leaveChat() {
  clearTimeout(state.reconnectTimer); clearTimeout(restoreTimer); state.socket?.close(); state.socket = null; state.account = null; state.bootstrap = null; state.activeConversation = null;
  chatShell.hidden = true; authShell.hidden = false; messages.replaceChildren(loadEarlier); userList.textContent = ""; state.messageIds.clear(); lastReadSent = new Map();
}

const mode = new URLSearchParams(location.search).get("auth");
if (mode) history.replaceState(null, "", location.pathname);
if (mode === "recovery") { authShell.hidden = false; chatShell.hidden = true; showView("reset-view"); }
else { if (mode === "verified") showNotice("Email verified. Your account is ready.", "success"); if (mode === "expired_link" || mode === "invalid_link") showNotice("That email link is invalid or expired.", "error"); loadAccount(); }
