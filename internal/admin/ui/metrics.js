"use strict";

const { createApp } = Vue;

function formatInt(value) {
  return Number(value || 0).toLocaleString("en-US");
}

createApp({
  data() {
    return {
      authEnabled: false,
      loading: false,
      loadError: "",
      autoRefresh: true,
      timer: null,
      lastUpdatedText: "--:--:--",
      rawProm: "",
      overviewCards: [],
      statusRows: [],
      tokenRows: [],
      usageWindow: "7d",
      usageError: "",
      usageUserRows: [],
      usageProviderRows: [],
    };
  },

  watch: {
    autoRefresh(enabled) {
      if (enabled) this.startAuto();
      else this.stopAuto();
    },
  },

  methods: {
    async initialize() {
      const auth = await window.adminUtils.fetchAuthStatus().catch(() => ({ enabled: false }));
      this.authEnabled = !!auth.enabled;
      await this.refresh();
      if (this.autoRefresh) this.startAuto();
    },

    async fetchMetrics() {
      const [json, prom] = await Promise.all([
        apiFetch("/metrics").then((r) => r.json()),
        apiFetch("/metrics?format=prometheus").then((r) => r.text()),
      ]);
      return { json, prom };
    },

    buildRows(json, prom) {
      const tokenUsage = json.token_usage_by_provider || {};
      const tokenRows = [];
      let totalPrompt = 0;
      let totalCompletion = 0;

      for (const [provider, usage] of Object.entries(tokenUsage)) {
        const prompt = Number(usage.prompt_tokens || 0);
        const completion = Number(usage.completion_tokens || 0);
        const total = prompt + completion;
        totalPrompt += prompt;
        totalCompletion += completion;
        tokenRows.push({
          provider,
          prompt: formatInt(prompt),
          completion: formatInt(completion),
          total: formatInt(total),
          totalValue: total,
        });
      }
      tokenRows.sort((a, b) => b.totalValue - a.totalValue);

      const statuses = json.responses_by_status || {};
      const statusRows = Object.keys(statuses).sort().map((status) => ({
        status,
        count: formatInt(statuses[status]),
        tagType: this.statusTagType(status),
      }));

      let successCount = 0;
      let errorCount = 0;
      for (const [status, count] of Object.entries(statuses)) {
        const code = Number(status);
        const numericCount = Number(count || 0);
        if (!Number.isNaN(code) && code >= 200 && code < 300) successCount += numericCount;
        if (!Number.isNaN(code) && code >= 400) errorCount += numericCount;
      }

      this.overviewCards = [
        { label: "请求总数", value: formatInt(json.requests_total || 0), meta: `2xx 成功 ${formatInt(successCount)}` },
        { label: "Prompt Tokens", value: formatInt(totalPrompt), meta: `已追踪 provider ${formatInt(tokenRows.length)}` },
        { label: "Completion Tokens", value: formatInt(totalCompletion), meta: `错误响应 ${formatInt(errorCount)}` },
        { label: "Total Tokens", value: formatInt(totalPrompt + totalCompletion), meta: "近实时聚合" },
      ];
      this.statusRows = statusRows;
      this.tokenRows = tokenRows;
      this.rawProm = prom;
    },

    statusTagType(status) {
      const code = Number(status);
      if (Number.isNaN(code)) return "info";
      if (code >= 200 && code < 300) return "success";
      if (code >= 300 && code < 400) return "warning";
      if (code >= 400) return "danger";
      return "info";
    },

    async refresh() {
      this.loading = true;
      this.loadError = "";
      try {
        const { json, prom } = await this.fetchMetrics();
        this.buildRows(json, prom);
        this.lastUpdatedText = new Date().toLocaleTimeString();
      } catch (err) {
        this.loadError = String(err.message || err);
      } finally {
        this.loading = false;
      }
      // Usage summary is paced independently of the Prometheus poll (expensive
      // aggregation vs. cheap counter scrape) so we refresh it on the same
      // cadence but don't await failure propagation into the metrics banner.
      await this.refreshUsage();
    },

    async refreshUsage() {
      this.usageError = "";
      try {
        const r = await apiFetch(`/api/usage/summary?window=${encodeURIComponent(this.usageWindow)}`);
        if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
        const summary = await r.json();
        this.usageUserRows = (summary.users || []).map((u) => ({
          user_id: u.user_id,
          user_name: u.user_name,
          requestsText: formatInt(u.requests),
          promptText: formatInt(u.prompt_tokens),
          completionText: formatInt(u.completion_tokens),
          totalText: formatInt(u.total_tokens),
        }));
        this.usageProviderRows = (summary.providers || []).map((p) => ({
          provider: p.provider,
          requestsText: formatInt(p.requests),
          promptText: formatInt(p.prompt_tokens),
          completionText: formatInt(p.completion_tokens),
          totalText: formatInt(p.total_tokens),
        }));
      } catch (err) {
        this.usageError = String(err.message || err);
      }
    },

    startAuto() {
      this.stopAuto();
      this.timer = setInterval(() => {
        this.refresh();
      }, 3000);
    },

    stopAuto() {
      if (this.timer != null) {
        clearInterval(this.timer);
        this.timer = null;
      }
    },

    async logout() {
      await window.adminUtils.logout();
    },
  },

  mounted() {
    this.initialize();
  },

  beforeUnmount() {
    this.stopAuto();
  },
}).use(ElementPlus).mount("#app");
