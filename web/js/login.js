// Sign-in page. On an empty database it becomes the registration page for
// the first account, which needs the setup token from the collector log.
import { h, replace } from "./dom.js";
import { post, get } from "./api.js";

try {
  const theme = localStorage.getItem("tikscope.theme");
  if (theme) document.documentElement.dataset.theme = theme;
} catch { /* theme stays on the system preference */ }

const form = document.getElementById("auth-form");
const fields = document.getElementById("auth-fields");
const error = document.getElementById("auth-error");
const submit = document.getElementById("auth-submit");
const sub = document.getElementById("auth-sub");

function field(label, name, type, attrs = {}) {
  return h("label", { class: "field" }, h("span", null, label), h("input", { name, type, required: true, ...attrs }));
}

function showError(msg) {
  error.textContent = msg;
  error.hidden = !msg;
}

async function start() {
  let status;
  try {
    status = await get("/api/auth/status");
  } catch (err) {
    sub.textContent = `Collector unreachable: ${err.message}`;
    return;
  }
  if (status.authenticated) {
    location.replace("/");
    return;
  }
  const setup = status.setup_required;
  document.title = setup ? "Create account, tikscope" : "Sign in, tikscope";
  sub.textContent = setup ? "Create the dashboard account" : "Sign in to the dashboard";
  submit.textContent = setup ? "Create account" : "Sign in";
  replace(fields,
    field("Username", "username", "text", { autocomplete: "username", autofocus: true, minlength: 3, maxlength: 32, spellcheck: false, autocapitalize: "none" }),
    field("Password", "password", "password", { autocomplete: setup ? "new-password" : "current-password", minlength: setup ? 12 : 1, maxlength: 256 }),
    setup ? field("Repeat password", "confirm", "password", { autocomplete: "new-password", minlength: 12, maxlength: 256 }) : null,
    setup ? field("Setup token", "token", "text", { autocomplete: "off", spellcheck: false, placeholder: "from the collector log" }) : null,
    setup ? h("p", { class: "form-hint" }, "At least 12 characters; a passphrase works well. The setup token is printed in the collector log at startup (docker compose logs tikscope).") : null);
  form.hidden = false;
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    showError("");
    const data = Object.fromEntries(new FormData(form).entries());
    if (setup && data.password !== data.confirm) {
      showError("The passwords do not match.");
      return;
    }
    submit.disabled = true;
    try {
      await post(setup ? "/api/auth/setup" : "/api/auth/login", { username: data.username, password: data.password, token: data.token });
      location.replace("/");
    } catch (err) {
      showError(err.message);
      submit.disabled = false;
    }
  });
}

start();
