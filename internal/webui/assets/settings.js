export function renderAccount(account, select) {
  const p = account.profile;
  select("#current-user").textContent = p.displayName;
  select("#current-handle").textContent = `@${p.username}`;
  select("#avatar").textContent = p.displayName.slice(0, 1).toUpperCase();
  select("#settings-email").textContent = account.email;
  select("#display-form [name=displayName]").value = p.displayName;
  select("#username-form [name=username]").value = p.username;
}
