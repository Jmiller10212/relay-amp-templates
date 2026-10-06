export function createNavigation(select) {
  function mobileContent(enabled, title = "Relay") {
    select("#chat-shell").classList.toggle("mobile-content", enabled);
    select("#mobile-title").textContent = title;
  }

  function destination(name, serverID = "") {
    const home = name === "home";
    const lobby = name === "lobby";
    select("#rail-home").classList.toggle("active", home);
    select("#rail-lobby").classList.toggle("active", lobby);
    document.querySelectorAll("[data-server-rail]").forEach((button) => button.classList.toggle("active", name === "server" && button.dataset.serverRail === serverID));
    select("#home-context").hidden = !home;
    select("#lobby-context").hidden = !lobby;
    select("#server-context").hidden = name !== "server";
  }

  function friends() {
    select("#friends-view").hidden = false;
    select("#server-invites-view").hidden = true;
    select("#conversation-view").hidden = true;
    select("#friends-destination").classList.add("active");
    mobileContent(true, "Friends");
  }

  function conversation(title) {
    select("#friends-view").hidden = true;
    select("#server-invites-view").hidden = true;
    select("#conversation-view").hidden = false;
    select("#friends-destination").classList.remove("active");
    mobileContent(true, title);
  }

  function invitations() {
    select("#friends-view").hidden = true;
    select("#conversation-view").hidden = true;
    select("#server-invites-view").hidden = false;
    mobileContent(true, "Server Invitations");
  }

  function updateHomeBadge(count) {
    const badge = select("#home-badge");
    badge.textContent = count > 99 ? "99+" : String(count);
    badge.hidden = count < 1;
  }

  select("#mobile-back").addEventListener("click", () => mobileContent(false));
  return {destination, friends, conversation, invitations, mobileContent, updateHomeBadge};
}
