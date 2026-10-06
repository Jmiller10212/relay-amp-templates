export function formPayload(form) {
  return Object.fromEntries(Array.from(form.querySelectorAll("[data-relay-field][name]"), (field) => [field.name, field.value]));
}
export function normalizeUsername(form) {
  const input = form.elements.username;
  if (input) input.value = input.value.normalize("NFKC").trim().toLowerCase().replace(/[^a-z0-9_]/g, "");
}
export function setFormBusy(form, busy) { form.querySelectorAll("button,input").forEach((el) => el.disabled = busy); }
export function setFormError(form, error) { form.querySelector(".form-error").textContent = error?.message || ""; }
