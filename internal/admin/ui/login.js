"use strict";

// If auth is disabled or we're already authenticated, jump straight to the
// main UI — no reason to show an empty login box.
fetch("/api/auth").then((r) => r.json()).then((s) => {
  if (!s.enabled || s.authenticated) location.replace("/ui/");
}).catch(() => {});

document.getElementById("login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = document.getElementById("login-error");
  err.textContent = "";
  const password = e.target.password.value;
  try {
    const r = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password }),
    });
    if (!r.ok) {
      err.textContent = r.status === 401 ? "密码错误" : (await r.text()).trim() || r.statusText;
      return;
    }
    location.replace("/ui/");
  } catch (x) {
    err.textContent = String(x.message || x);
  }
});
