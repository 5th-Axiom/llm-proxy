"use strict";

const api = {
  async listProviders() {
    const r = await fetch("/api/providers");
    if (!r.ok) throw new Error(await r.text());
    return r.json();
  },
  async summary() {
    const r = await fetch("/api/config");
    if (!r.ok) throw new Error(await r.text());
    return r.json();
  },
  async createProvider(p) {
    const r = await fetch("/api/providers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(p),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async updateProvider(name, p) {
    const r = await fetch(`/api/providers/${encodeURIComponent(name)}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(p),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async deleteProvider(name) {
    const r = await fetch(`/api/providers/${encodeURIComponent(name)}`, { method: "DELETE" });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  },
};

const editor = document.getElementById("editor");
const editorForm = document.getElementById("editor-form");
const editorError = document.getElementById("editor-error");
const editorTitle = document.getElementById("editor-title");
const apikeyHint = document.getElementById("apikey-hint");
const headersList = document.getElementById("headers-list");

function addHeaderRow(k = "", v = "") {
  const row = document.createElement("div");
  row.className = "header-row";
  row.innerHTML = `
    <input placeholder="header" class="hk" />
    <input placeholder="value" class="hv" />
    <button type="button">x</button>
  `;
  row.querySelector(".hk").value = k;
  row.querySelector(".hv").value = v;
  row.querySelector("button").addEventListener("click", () => row.remove());
  headersList.appendChild(row);
}

document.getElementById("add-header").addEventListener("click", () => addHeaderRow());

function openEditor(mode, provider) {
  editorForm.reset();
  headersList.innerHTML = "";
  editorError.textContent = "";

  if (mode === "create") {
    editorTitle.textContent = "新建 provider";
    editorForm.mode.value = "create";
    editorForm.original_name.value = "";
    editorForm.querySelector("[name=upstream_api_key]").required = true;
    apikeyHint.textContent = "必填（可写 ${ENV_VAR}）";
  } else {
    editorTitle.textContent = `编辑 ${provider.name}`;
    editorForm.mode.value = "update";
    editorForm.original_name.value = provider.name;
    editorForm.name.value = provider.name;
    editorForm.type.value = provider.type;
    editorForm.base_path.value = provider.base_path;
    editorForm.upstream_base_url.value = provider.upstream_base_url;
    editorForm.querySelector("[name=upstream_api_key]").required = false;
    apikeyHint.textContent = `当前: ${provider.api_key_preview || "(空)"}  — 留空保留原值`;
    if (provider.token_counting === true) editorForm.token_counting.value = "true";
    else if (provider.token_counting === false) editorForm.token_counting.value = "false";
    else editorForm.token_counting.value = "inherit";
    for (const [k, v] of Object.entries(provider.upstream_headers || {})) addHeaderRow(k, v);
  }

  editor.showModal();
}

document.getElementById("cancel").addEventListener("click", (e) => {
  e.preventDefault();
  editor.close();
});

document.getElementById("new-provider").addEventListener("click", () => openEditor("create"));

editorForm.addEventListener("submit", async (e) => {
  e.preventDefault();
  editorError.textContent = "";

  const headers = {};
  for (const row of headersList.querySelectorAll(".header-row")) {
    const k = row.querySelector(".hk").value.trim();
    const v = row.querySelector(".hv").value.trim();
    if (k) headers[k] = v;
  }

  const tcRaw = editorForm.token_counting.value;
  const payload = {
    name: editorForm.name.value.trim(),
    type: editorForm.type.value,
    base_path: editorForm.base_path.value.trim(),
    upstream_base_url: editorForm.upstream_base_url.value.trim(),
    upstream_api_key: editorForm.upstream_api_key.value,
    upstream_headers: Object.keys(headers).length ? headers : undefined,
    token_counting: tcRaw === "inherit" ? null : tcRaw === "true",
  };

  try {
    if (editorForm.mode.value === "create") {
      await api.createProvider(payload);
    } else {
      await api.updateProvider(editorForm.original_name.value, payload);
    }
    editor.close();
    await refresh();
  } catch (err) {
    editorError.textContent = String(err.message || err);
  }
});

async function refresh() {
  const [summary, providers] = await Promise.all([api.summary(), api.listProviders()]);
  const s = document.getElementById("summary");
  s.textContent = `listen=${summary.listen}  metrics=${summary.metrics_listen}  tc=${summary.token_counting_enabled}  providers=${summary.provider_count}  tokens=${summary.tokens.map(maskPreview).join(", ")}`;

  const tbody = document.querySelector("#providers-table tbody");
  tbody.innerHTML = "";
  for (const p of providers) {
    const tr = document.createElement("tr");
    const tc = p.token_counting === true ? "on" : p.token_counting === false ? "off" : "inherit";
    tr.innerHTML = `
      <td>${esc(p.name)}</td>
      <td>${esc(p.type)}</td>
      <td><code>${esc(p.base_path)}</code></td>
      <td><code>${esc(p.upstream_base_url)}</code></td>
      <td><code>${esc(p.api_key_preview)}</code></td>
      <td>${tc}</td>
      <td><div class="actions">
        <button data-act="edit">编辑</button>
        <button data-act="delete" class="danger">删除</button>
      </div></td>`;
    tr.querySelector("[data-act=edit]").addEventListener("click", () => openEditor("update", p));
    tr.querySelector("[data-act=delete]").addEventListener("click", async () => {
      if (!confirm(`删除 provider "${p.name}"？`)) return;
      try {
        await api.deleteProvider(p.name);
        await refresh();
      } catch (err) { alert(err.message || err); }
    });
    tbody.appendChild(tr);
  }
}

function esc(s) {
  return (s ?? "").toString().replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
function maskPreview(t) {
  if (!t) return "";
  if (t.length <= 6) return "••••";
  return "••••" + t.slice(-4);
}

refresh().catch((err) => {
  document.querySelector("main").insertAdjacentHTML("afterbegin",
    `<div class="error">加载失败：${esc(err.message || err)}</div>`);
});
