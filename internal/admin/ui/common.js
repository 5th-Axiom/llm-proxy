"use strict";

window.apiFetch = async function apiFetch(url, opts) {
  const r = await fetch(url, opts);
  if (r.status === 401 && !location.pathname.endsWith("/login.html")) {
    location.replace("/ui/login.html");
    throw new Error("unauthorized");
  }
  return r;
};

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

window.adminUtils = {
  async fetchAuthStatus() {
    const r = await fetch("/api/auth");
    return r.json();
  },

  async ensureAuthenticated() {
    try {
      const auth = await this.fetchAuthStatus();
      if (auth.enabled && !auth.authenticated) {
        location.replace("/ui/login.html");
        return auth;
      }
      return auth;
    } catch (_) {
      return { enabled: false, authenticated: false };
    }
  },

  async logout() {
    await fetch("/api/logout", { method: "POST" });
    location.replace("/ui/login.html");
  },
};

(async function initAuthGate() {
  if (location.pathname.endsWith("/login.html")) return;
  await window.adminUtils.ensureAuthenticated();
})();
