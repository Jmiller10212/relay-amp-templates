export async function api(path, options = {}) {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...options,
    headers: {"Content-Type": "application/json", ...(options.headers || {})},
  });
  let body = null;
  try { body = await response.json(); } catch (_) {}
  if (!response.ok) {
    const error = new Error(body?.error?.message || "Relay could not complete that request.");
    error.code = body?.error?.code;
    error.field = body?.error?.field;
    error.status = response.status;
    throw error;
  }
  return body;
}
