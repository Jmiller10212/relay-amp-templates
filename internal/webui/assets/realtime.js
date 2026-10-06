export class RelayRealtime {
  constructor(onEvent, onState) {
    this.onEvent = onEvent;
    this.onState = onState;
    this.socket = null;
  }
  connect() {
    if (this.socket && this.socket.readyState < WebSocket.CLOSING) return;
    const protocol = location.protocol === "https:" ? "wss:" : "ws:";
    this.socket = new WebSocket(`${protocol}//${location.host}/api/v1/realtime`);
    this.socket.addEventListener("open", () => this.onState("open"));
    this.socket.addEventListener("message", (event) => {
      try { this.onEvent(JSON.parse(event.data)); } catch (_) {}
    });
    this.socket.addEventListener("close", () => { this.socket = null; this.onState("closed"); });
  }
  close() { this.socket?.close(); this.socket = null; }
}
