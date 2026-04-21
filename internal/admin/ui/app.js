"use strict";

const { createApp } = Vue;
const { ElMessage, ElMessageBox } = ElementPlus;

const api = {
  async listProviders() {
    const r = await apiFetch("/api/providers");
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },

  async summary() {
    const r = await apiFetch("/api/config");
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },

  async createProvider(payload) {
    const r = await apiFetch("/api/providers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },

  async updateProvider(name, payload) {
    const r = await apiFetch(`/api/providers/${encodeURIComponent(name)}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },

  async deleteProvider(name) {
    const r = await apiFetch(`/api/providers/${encodeURIComponent(name)}`, {
      method: "DELETE",
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  },
};

function emptyEditorForm() {
  return {
    name: "",
    type: "openai",
    base_path: "",
    upstream_base_url: "",
    upstream_api_key: "",
    token_counting: "inherit",
    upstream_headers: [],
  };
}

createApp({
  data() {
    return {
      authEnabled: false,
      loading: false,
      saving: false,
      loadError: "",
      summary: {
        listen: "-",
        metrics_listen: "-",
        provider_count: 0,
        token_counting_enabled: false,
        tokens: [],
      },
      providers: [],
      editor: {
        visible: false,
        mode: "create",
        originalName: "",
        error: "",
        form: emptyEditorForm(),
      },
      tokenCountingOptions: [
        { label: "inherit", value: "inherit" },
        { label: "force on", value: "true" },
        { label: "force off", value: "false" },
      ],
    };
  },

  computed: {
    summaryCards() {
      return [
        { label: "Admin Listen", value: this.summary.listen || "-" },
        { label: "Metrics Listen", value: this.summary.metrics_listen || "-" },
        { label: "Providers", value: String(this.summary.provider_count || 0) },
        { label: "Token Counting", value: this.summary.token_counting_enabled ? "enabled" : "disabled" },
        { label: "Server Tokens", value: (this.summary.tokens || []).join(", ") || "none" },
      ];
    },

    apiKeyHint() {
      if (this.editor.mode === "create") return "创建时必填，可直接填写 ${ENV_VAR}。";
      return this.editor.form.upstream_api_key
        ? "保存后将用新值覆盖现有 API key。"
        : "留空时保留当前值。";
    },
  },

  methods: {
    async initialize() {
      const auth = await window.adminUtils.fetchAuthStatus().catch(() => ({ enabled: false }));
      this.authEnabled = !!auth.enabled;
      await this.refresh();
    },

    async refresh() {
      this.loading = true;
      this.loadError = "";
      try {
        const [summary, providers] = await Promise.all([api.summary(), api.listProviders()]);
        summary.tokens = (summary.tokens || []).map(maskPreview);
        this.summary = summary;
        this.providers = providers;
      } catch (err) {
        this.loadError = String(err.message || err);
      } finally {
        this.loading = false;
      }
    },

    tokenCountingLabel(value) {
      if (value === true) return "force on";
      if (value === false) return "force off";
      return "inherit";
    },

    proxyBaseURL() {
      // server.listen can be ":8080" (all-interfaces) or "host:port". In
      // the wildcard case we guess 127.0.0.1 so the snippet is copy-paste
      // runnable from the same host the admin browser is on.
      const listen = (this.summary && this.summary.listen) || ":8080";
      if (listen.startsWith(":")) return "http://127.0.0.1" + listen;
      return "http://" + listen;
    },

    snippetPath(type) {
      // Map provider type to the canonical endpoint clients hit. The proxy
      // itself accepts anything under base_path/v1/... but these are the
      // ones people actually care about for a first smoke-test.
      if (type === "anthropic") return "/v1/messages";
      return "/v1/chat/completions";
    },

    snippetTitle(row) {
      const path = (row.base_path || "") + this.snippetPath(row.type);
      return `${row.type} POST ${path}`;
    },

    snippetBody(type) {
      if (type === "anthropic") {
        return `{"model":"<MODEL>","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`;
      }
      return `{"model":"<MODEL>","messages":[{"role":"user","content":"hi"}]}`;
    },

    snippetAuthHeader(type) {
      // Both forms are accepted by the proxy; anthropic clients usually
      // reach for x-api-key, openai clients for Authorization. Match the
      // user's muscle memory so the snippet is obvious.
      if (type === "anthropic") return `-H 'x-api-key: <PROXY_TOKEN>'`;
      return `-H 'Authorization: Bearer <PROXY_TOKEN>'`;
    },

    buildCurl(row) {
      const url = this.proxyBaseURL() + row.base_path + this.snippetPath(row.type);
      const lines = [
        `curl ${url} \\`,
        `  ${this.snippetAuthHeader(row.type)} \\`,
        `  -H 'Content-Type: application/json' \\`,
        `  -d '${this.snippetBody(row.type)}'`,
      ];
      return lines.join("\n");
    },

    async copySnippet(row) {
      try {
        await navigator.clipboard.writeText(this.buildCurl(row));
        ElMessage.success("已复制到剪贴板");
      } catch (_) {
        ElMessage.warning("无法访问剪贴板，请手动选择复制");
      }
    },

    tokenCountingType(value) {
      if (value === true) return "success";
      if (value === false) return "danger";
      return "warning";
    },

    openCreate() {
      this.editor.visible = true;
      this.editor.mode = "create";
      this.editor.originalName = "";
      this.editor.error = "";
      this.editor.form = emptyEditorForm();
    },

    openEdit(provider) {
      const headers = Object.entries(provider.upstream_headers || {}).map(([key, value]) => ({ key, value }));
      this.editor.visible = true;
      this.editor.mode = "update";
      this.editor.originalName = provider.name;
      this.editor.error = "";
      this.editor.form = {
        name: provider.name,
        type: provider.type,
        base_path: provider.base_path,
        upstream_base_url: provider.upstream_base_url,
        upstream_api_key: "",
        token_counting: provider.token_counting === true ? "true" : provider.token_counting === false ? "false" : "inherit",
        upstream_headers: headers,
      };
    },

    addHeaderRow() {
      this.editor.form.upstream_headers.push({ key: "", value: "" });
    },

    removeHeaderRow(index) {
      this.editor.form.upstream_headers.splice(index, 1);
    },

    buildPayload() {
      const form = this.editor.form;
      const headers = {};
      for (const item of form.upstream_headers) {
        const key = (item.key || "").trim();
        const value = (item.value || "").trim();
        if (key) headers[key] = value;
      }

      return {
        name: form.name.trim(),
        type: form.type,
        base_path: form.base_path.trim(),
        upstream_base_url: form.upstream_base_url.trim(),
        upstream_api_key: form.upstream_api_key,
        upstream_headers: Object.keys(headers).length ? headers : undefined,
        token_counting: form.token_counting === "inherit" ? null : form.token_counting === "true",
      };
    },

    validatePayload(payload) {
      if (!payload.name) return "name 不能为空";
      if (!payload.type) return "type 不能为空";
      if (!payload.base_path) return "base_path 不能为空";
      if (!payload.upstream_base_url) return "upstream_base_url 不能为空";
      if (this.editor.mode === "create" && !payload.upstream_api_key.trim()) {
        return "创建 provider 时 upstream_api_key 必填";
      }
      return "";
    },

    async saveProvider() {
      const payload = this.buildPayload();
      this.editor.error = this.validatePayload(payload);
      if (this.editor.error) return;

      this.saving = true;
      try {
        if (this.editor.mode === "create") {
          await api.createProvider(payload);
          ElMessage.success("Provider 已创建并热加载");
        } else {
          await api.updateProvider(this.editor.originalName, payload);
          ElMessage.success("Provider 已更新并热加载");
        }
        this.editor.visible = false;
        await this.refresh();
      } catch (err) {
        this.editor.error = String(err.message || err);
      } finally {
        this.saving = false;
      }
    },

    async removeProvider(provider) {
      try {
        await ElMessageBox.confirm(
          `删除 provider "${provider.name}" 后会立即写回配置并生效，是否继续？`,
          "确认删除",
          {
            confirmButtonText: "删除",
            cancelButtonText: "取消",
            type: "warning",
          }
        );
      } catch (_) {
        return;
      }

      try {
        await api.deleteProvider(provider.name);
        ElMessage.success("Provider 已删除");
        await this.refresh();
      } catch (err) {
        ElMessage.error(String(err.message || err));
      }
    },

    async logout() {
      await window.adminUtils.logout();
    },
  },

  mounted() {
    this.initialize();
  },
}).use(ElementPlus).mount("#app");
