"use strict";

// Redirect to the login page when an authenticated-only request comes back
// 401. This keeps session expiry from dropping users into a confusing "it
// just stopped working" state.
window.apiFetch = async function apiFetch(url, opts) {
  const r = await fetch(url, opts);
  if (r.status === 401 && !location.pathname.endsWith("/login.html")) {
    location.replace("/ui/login.html");
    throw new Error("unauthorized");
  }
  return r;
};

(async function initChrome() {
  try {
    const r = await fetch("/api/auth");
    const s = await r.json();
    const btn = document.getElementById("logout");
    if (btn && s.enabled) {
      btn.hidden = false;
      btn.addEventListener("click", async () => {
        await fetch("/api/logout", { method: "POST" });
        location.replace("/ui/login.html");
      });
    }
    if (s.enabled && !s.authenticated) {
      location.replace("/ui/login.html");
    }
  } catch (e) {
    // best-effort chrome; keep the page usable if /api/auth fails transiently
  }
})();

window.escHTML = function (s) {
  return (s ?? "").toString().replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
};

window.maskPreview = function (t) {
  if (!t) return "";
  if (t.length <= 6) return "••••";
  return "••••" + t.slice(-4);
};
