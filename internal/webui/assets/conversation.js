function messageElement(message) {
  const row = document.createElement("article");
  row.className = `message ${message.kind === "system" ? "system" : ""}`;
  row.dataset.messageId = String(message.id);
  const avatar = document.createElement("div");
  avatar.className = "message-avatar";
  const name = message.displayName || message.username;
  avatar.textContent = name.slice(0, 1).toUpperCase();
  const body = document.createElement("div");
  const meta = document.createElement("div");
  const strong = document.createElement("strong");
  const handle = document.createElement("span");
  const time = document.createElement("time");
  const text = document.createElement("div");
  body.className = "message-body";
  meta.className = "message-meta";
  strong.textContent = name;
  handle.textContent = message.kind === "user" ? `@${message.username}` : "";
  time.dateTime = message.createdAt;
  time.textContent = new Date(message.createdAt).toLocaleString();
  text.className = "message-text";
  text.textContent = message.text;
  meta.append(strong, handle, time);
  body.append(meta, text);
  row.append(avatar, body);
  if (message.kind === "user") {
    const pin = document.createElement("button");
    pin.type = "button";
    pin.className = "message-pin-action";
    pin.dataset.pinMessage = String(message.id);
    pin.setAttribute("aria-label", "Pin message");
    pin.title = "Pin message";
    pin.textContent = "◆";
    row.append(pin);
  }
  return row;
}

export function addMessage(container, knownIds, message, options = {}) {
  if (knownIds.has(message.id)) return false;
  knownIds.add(message.id);
  const row = messageElement(message);
  if (options.prependBefore) container.insertBefore(row, options.prependBefore);
  else container.append(row);
  if (options.scroll !== false) container.scrollTop = container.scrollHeight;
  return true;
}

export function prependMessages(container, knownIds, messages, anchor) {
  const oldHeight = container.scrollHeight;
  const fragment = document.createDocumentFragment();
  messages.forEach((message) => {
    if (knownIds.has(message.id)) return;
    knownIds.add(message.id);
    fragment.append(messageElement(message));
  });
  container.insertBefore(fragment, anchor);
  container.scrollTop += container.scrollHeight - oldHeight;
}

export function newestMessageId(container) {
  const rows = container.querySelectorAll("[data-message-id]");
  return rows.length ? Number(rows[rows.length - 1].dataset.messageId) : 0;
}

export function renderUsers(container, counter, users) {
  container.textContent = "";
  counter.textContent = String(users.length);
  users.forEach((user) => {
    const li = document.createElement("li");
    const dot = document.createElement("span");
    const names = document.createElement("div");
    const display = document.createElement("strong");
    const handle = document.createElement("small");
    dot.className = "online-dot";
    display.textContent = user.displayName;
    handle.textContent = `@${user.username}`;
    names.append(display, handle);
    li.append(dot, names);
    container.append(li);
  });
}
