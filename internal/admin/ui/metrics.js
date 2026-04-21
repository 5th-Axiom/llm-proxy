"use strict";

async function fetchMetrics() {
  const [json, prom] = await Promise.all([
    apiFetch("/metrics").then((r) => r.json()),
    apiFetch("/metrics?format=prometheus").then((r) => r.text()),
  ]);
  return { json, prom };
}

function render(data) {
  const { json, prom } = data;

  document.getElementById("last-updated").textContent =
    new Date().toLocaleTimeString();

  const tokenUsage = json.token_usage_by_provider || {};
  let totalPrompt = 0;
  let totalCompletion = 0;
  const tokenRows = [];
  for (const [provider, usage] of Object.entries(tokenUsage)) {
    const p = Number(usage.prompt_tokens || 0);
    const c = Number(usage.completion_tokens || 0);
    totalPrompt += p;
    totalCompletion += c;
    tokenRows.push({ provider, p, c, t: p + c });
  }
  tokenRows.sort((a, b) => b.t - a.t);

  // cards
  const cards = document.getElementById("cards");
  cards.innerHTML = "";
  addCard(cards, "请求总数", formatInt(json.requests_total || 0));
  addCard(cards, "prompt tokens", formatInt(totalPrompt));
  addCard(cards, "completion tokens", formatInt(totalCompletion));

  // status table
  const statuses = json.responses_by_status || {};
  const stBody = document.querySelector("#status-table tbody");
  stBody.innerHTML = "";
  const statusKeys = Object.keys(statuses).sort();
  if (statusKeys.length === 0) {
    stBody.innerHTML = `<tr><td colspan="2" class="summary">暂无数据</td></tr>`;
  } else {
    for (const k of statusKeys) {
      const tr = document.createElement("tr");
      tr.innerHTML = `<td><code>${escHTML(k)}</code></td><td>${formatInt(statuses[k])}</td>`;
      stBody.appendChild(tr);
    }
  }

  // token table
  const tBody = document.querySelector("#token-table tbody");
  tBody.innerHTML = "";
  const empty = document.getElementById("token-empty");
  if (tokenRows.length === 0) {
    empty.hidden = false;
  } else {
    empty.hidden = true;
    for (const row of tokenRows) {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td>${escHTML(row.provider)}</td>
        <td>${formatInt(row.p)}</td>
        <td>${formatInt(row.c)}</td>
        <td>${formatInt(row.t)}</td>`;
      tBody.appendChild(tr);
    }
  }

  document.getElementById("raw-prom").textContent = prom;
}

function addCard(parent, label, value) {
  const d = document.createElement("div");
  d.className = "card";
  d.innerHTML = `<div class="card-value">${escHTML(value)}</div><div class="card-label">${escHTML(label)}</div>`;
  parent.appendChild(d);
}

function formatInt(n) {
  return Number(n).toLocaleString("en-US");
}

let timer = null;
function startAuto() {
  stopAuto();
  timer = setInterval(refresh, 3000);
}
function stopAuto() {
  if (timer != null) {
    clearInterval(timer);
    timer = null;
  }
}

async function refresh() {
  try {
    render(await fetchMetrics());
  } catch (e) {
    // best-effort UI; an unauthorized response already redirected us
  }
}

document.getElementById("auto").addEventListener("change", (e) => {
  if (e.target.checked) startAuto();
  else stopAuto();
});

refresh().then(() => {
  if (document.getElementById("auto").checked) startAuto();
});
