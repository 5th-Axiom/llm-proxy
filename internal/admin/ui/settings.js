"use strict";

const { createApp } = Vue;
const { ElMessage, ElMessageBox } = ElementPlus;

const api = {
  async load() {
    const r = await apiFetch("/api/settings");
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async patch(payload) {
    const r = await apiFetch("/api/settings", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async changePassword(payload) {
    const r = await apiFetch("/api/settings/password", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  },
  async regenerateMetricsToken() {
    const r = await apiFetch("/api/settings/metrics-token", { method: "POST" });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async clearMetricsToken() {
    const r = await apiFetch("/api/settings/metrics-token", { method: "DELETE" });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  },
  async cleanupNow() {
    const r = await apiFetch("/api/usage/cleanup", { method: "POST" });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
};

createApp({
  data() {
    return {
      authEnabled: false,
      loading: false,
      loadError: "",
      view: {
        token_counting_enabled: false,
        usage_retention_days: 0,
        session_ttl_min: 0,
        password_configured: false,
        metrics_bearer_token_set: false,
      },
      form: {
        token_counting_enabled: false,
        usage_retention_days: 0,
        session_ttl_min: 0,
      },
      // Debounce a tick so a switch-toggle + a numeric change fired in the
      // same frame coalesce into one PATCH.
      pendingSave: null,
      cleaningUp: false,
      lastCleanup: "",
      metricsTokenBusy: false,
      passwordDialog: {
        visible: false,
        saving: false,
        error: "",
        current: "",
        next: "",
        confirm: "",
      },
      tokenDialog: {
        visible: false,
        token: "",
      },
    };
  },

  methods: {
    async initialize() {
      const auth = await window.adminUtils.fetchAuthStatus().catch(() => ({ enabled: false }));
      this.authEnabled = !!auth.enabled;
      await this.load();
    },

    async load() {
      this.loading = true;
      this.loadError = "";
      try {
        const v = await api.load();
        this.view = v;
        this.form.token_counting_enabled = v.token_counting_enabled;
        this.form.usage_retention_days = v.usage_retention_days;
        this.form.session_ttl_min = v.session_ttl_min;
      } catch (err) {
        this.loadError = String(err.message || err);
      } finally {
        this.loading = false;
      }
    },

    saveRuntime() {
      if (this.pendingSave) clearTimeout(this.pendingSave);
      this.pendingSave = setTimeout(async () => {
        this.pendingSave = null;
        try {
          const v = await api.patch({
            token_counting_enabled: this.form.token_counting_enabled,
            usage_retention_days: Number(this.form.usage_retention_days || 0),
            session_ttl_min: Number(this.form.session_ttl_min || 0),
          });
          this.view = v;
          ElMessage.success("已保存并热加载");
        } catch (err) {
          ElMessage.error(String(err.message || err));
          await this.load();
        }
      }, 250);
    },

    async cleanupNow() {
      this.cleaningUp = true;
      try {
        const r = await api.cleanupNow();
        this.lastCleanup = new Date().toLocaleTimeString();
        if (r.skipped) {
          ElMessage.info(`跳过：${r.skipped}`);
        } else {
          ElMessage.success(`已删除 ${r.deleted} 行`);
        }
      } catch (err) {
        ElMessage.error(String(err.message || err));
      } finally {
        this.cleaningUp = false;
      }
    },

    openPasswordDialog() {
      this.passwordDialog.visible = true;
      this.passwordDialog.saving = false;
      this.passwordDialog.error = "";
      this.passwordDialog.current = "";
      this.passwordDialog.next = "";
      this.passwordDialog.confirm = "";
    },

    async submitPassword() {
      const d = this.passwordDialog;
      d.error = "";
      if (!d.next) { d.error = "新密码不能为空"; return; }
      if (d.next.length < 8) { d.error = "新密码至少 8 位"; return; }
      if (d.next !== d.confirm) { d.error = "两次输入的新密码不一致"; return; }
      if (this.view.password_configured && !d.current) { d.error = "需要提供当前密码"; return; }

      d.saving = true;
      try {
        await api.changePassword({
          current_password: d.current,
          new_password: d.next,
        });
        ElMessage.success("密码已更新，其它设备上的 session 已失效");
        d.visible = false;
        await this.load();
      } catch (err) {
        d.error = String(err.message || err);
      } finally {
        d.saving = false;
      }
    },

    async regenerateMetricsToken() {
      if (this.view.metrics_bearer_token_set) {
        try {
          await ElMessageBox.confirm(
            "重新生成会让旧 token 立即失效，Prometheus 需要同步更新。继续吗？",
            "确认重新生成",
            { confirmButtonText: "重新生成", cancelButtonText: "取消", type: "warning" },
          );
        } catch (_) { return; }
      }
      this.metricsTokenBusy = true;
      try {
        const resp = await api.regenerateMetricsToken();
        this.tokenDialog.token = resp.token;
        this.tokenDialog.visible = true;
        await this.load();
      } catch (err) {
        ElMessage.error(String(err.message || err));
      } finally {
        this.metricsTokenBusy = false;
      }
    },

    async clearMetricsToken() {
      try {
        await ElMessageBox.confirm(
          "清除后 /metrics 只接受带登录 session 的请求；现有 Prometheus scrape 会 401。继续吗？",
          "确认清除",
          { confirmButtonText: "清除", cancelButtonText: "取消", type: "warning" },
        );
      } catch (_) { return; }
      this.metricsTokenBusy = true;
      try {
        await api.clearMetricsToken();
        ElMessage.success("已清除");
        await this.load();
      } catch (err) {
        ElMessage.error(String(err.message || err));
      } finally {
        this.metricsTokenBusy = false;
      }
    },

    async copyToken() {
      try {
        await navigator.clipboard.writeText(this.tokenDialog.token);
        ElMessage.success("已复制到剪贴板");
      } catch (_) {
        ElMessage.warning("无法访问剪贴板，请手动选择复制");
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
