"use strict";

const { createApp } = Vue;
const { ElMessage, ElMessageBox } = ElementPlus;

const api = {
  async listUsers() {
    const r = await apiFetch("/api/users");
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async createUser(payload) {
    const r = await apiFetch("/api/users", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async updateUser(id, payload) {
    const r = await apiFetch(`/api/users/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async setUserDisabled(id, disabled) {
    const r = await apiFetch(`/api/users/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ disabled }),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  },
  async listKeys(userID) {
    const r = await apiFetch(`/api/users/${userID}/keys`);
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async issueKey(userID, name) {
    const r = await apiFetch(`/api/users/${userID}/keys`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    return r.json();
  },
  async revokeKey(prefix) {
    const r = await apiFetch(`/api/keys/${encodeURIComponent(prefix)}/revoke`, {
      method: "POST",
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  },
  async deleteKey(prefix) {
    const r = await apiFetch(`/api/keys/${encodeURIComponent(prefix)}`, {
      method: "DELETE",
    });
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  },
  async revealKey(prefix) {
    const r = await apiFetch(`/api/keys/${encodeURIComponent(prefix)}/reveal`);
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
      users: [],
      userDialog: {
        visible: false,
        saving: false,
        mode: "create",
        originalID: 0,
        error: "",
        form: { name: "", email: "" },
      },
      keysDrawer: {
        visible: false,
        loading: false,
        user: null,
        keys: [],
      },
      issueDialog: {
        visible: false,
        saving: false,
        name: "",
      },
      tokenDialog: {
        visible: false,
        token: "",
        title: "",
        warn: "",
      },
    };
  },

  computed: {
    totalActiveKeys() {
      return this.users.reduce((sum, u) => sum + (u.key_count || 0), 0);
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
        this.users = await api.listUsers();
      } catch (err) {
        this.loadError = String(err.message || err);
      } finally {
        this.loading = false;
      }
    },

    openCreateUser() {
      this.userDialog.visible = true;
      this.userDialog.mode = "create";
      this.userDialog.originalID = 0;
      this.userDialog.form = { name: "", email: "" };
      this.userDialog.error = "";
    },

    openEditUser(user) {
      this.userDialog.visible = true;
      this.userDialog.mode = "update";
      this.userDialog.originalID = user.id;
      this.userDialog.form = { name: user.name, email: user.email || "" };
      this.userDialog.error = "";
    },

    async saveUser() {
      const form = this.userDialog.form;
      if (!form.name.trim()) {
        this.userDialog.error = "name 不能为空";
        return;
      }
      this.userDialog.saving = true;
      this.userDialog.error = "";
      try {
        if (this.userDialog.mode === "create") {
          await api.createUser({ name: form.name.trim(), email: form.email.trim() });
          ElMessage.success("用户已创建");
        } else {
          await api.updateUser(this.userDialog.originalID, {
            name: form.name.trim(),
            email: form.email.trim(),
          });
          ElMessage.success("用户已更新");
        }
        this.userDialog.visible = false;
        await this.refresh();
      } catch (err) {
        this.userDialog.error = String(err.message || err);
      } finally {
        this.userDialog.saving = false;
      }
    },

    async disableUser(user) {
      try {
        await ElMessageBox.confirm(
          `禁用用户 "${user.name}" 后，他持有的所有 key 都立即失效，是否继续？`,
          "确认禁用",
          { confirmButtonText: "禁用", cancelButtonText: "取消", type: "warning" },
        );
      } catch (_) { return; }
      try {
        await api.setUserDisabled(user.id, true);
        ElMessage.success("已禁用");
        await this.refresh();
      } catch (err) { ElMessage.error(String(err.message || err)); }
    },

    async enableUser(user) {
      try {
        await api.setUserDisabled(user.id, false);
        ElMessage.success("已恢复");
        await this.refresh();
      } catch (err) { ElMessage.error(String(err.message || err)); }
    },

    async openKeysDrawer(user) {
      this.keysDrawer.visible = true;
      this.keysDrawer.user = user;
      this.keysDrawer.keys = [];
      await this.reloadKeys();
    },

    async reloadKeys() {
      if (!this.keysDrawer.user) return;
      this.keysDrawer.loading = true;
      try {
        this.keysDrawer.keys = await api.listKeys(this.keysDrawer.user.id);
      } catch (err) {
        ElMessage.error(String(err.message || err));
      } finally {
        this.keysDrawer.loading = false;
      }
    },

    openIssueKey() {
      this.issueDialog.visible = true;
      this.issueDialog.name = "";
    },

    async issueKey() {
      if (!this.keysDrawer.user) return;
      this.issueDialog.saving = true;
      try {
        const resp = await api.issueKey(this.keysDrawer.user.id, this.issueDialog.name.trim());
        this.issueDialog.visible = false;
        this.showToken(
          "保管好这把新 key",
          resp.token,
          "明文只显示这一次，关闭后可随时回到列表里点“查看”重新展开。",
        );
        await this.reloadKeys();
        await this.refresh();
      } catch (err) {
        ElMessage.error(String(err.message || err));
      } finally {
        this.issueDialog.saving = false;
      }
    },

    showToken(title, token, warn) {
      this.tokenDialog.title = title;
      this.tokenDialog.token = token;
      this.tokenDialog.warn = warn || "";
      this.tokenDialog.visible = true;
    },

    async copyKey(key) {
      try {
        const resp = await api.revealKey(key.token_prefix);
        await navigator.clipboard.writeText(resp.token);
        ElMessage.success("已复制到剪贴板");
      } catch (err) {
        ElMessage.error(String(err.message || err));
      }
    },

    async revealKey(key) {
      try {
        const resp = await api.revealKey(key.token_prefix);
        this.showToken(`${key.name || "key"} · 明文`, resp.token, "");
      } catch (err) {
        ElMessage.error(String(err.message || err));
      }
    },

    async revokeKey(key) {
      try {
        await ElMessageBox.confirm(
          `撤销 key "${key.token_prefix}" 后，使用此 key 的调用会立即 401。撤销后仍可在列表里“删除”彻底移除。`,
          "确认撤销",
          { confirmButtonText: "撤销", cancelButtonText: "取消", type: "warning" },
        );
      } catch (_) { return; }
      try {
        await api.revokeKey(key.token_prefix);
        ElMessage.success("已撤销");
        await this.reloadKeys();
        await this.refresh();
      } catch (err) { ElMessage.error(String(err.message || err)); }
    },

    async deleteKey(key) {
      try {
        await ElMessageBox.confirm(
          `彻底删除 key "${key.token_prefix}"？此操作不可撤销；相关 usage 记录的 key_id 会置空（仍按 user 归集）。`,
          "确认删除",
          { confirmButtonText: "删除", cancelButtonText: "取消", type: "error" },
        );
      } catch (_) { return; }
      try {
        await api.deleteKey(key.token_prefix);
        ElMessage.success("已删除");
        await this.reloadKeys();
        await this.refresh();
      } catch (err) { ElMessage.error(String(err.message || err)); }
    },

    async copyToken() {
      try {
        await navigator.clipboard.writeText(this.tokenDialog.token);
        ElMessage.success("已复制到剪贴板");
      } catch (_) {
        ElMessage.warning("无法访问剪贴板，请手动选择复制");
      }
    },

    formatDateTime(v) {
      if (!v) return "—";
      try {
        return new Date(v).toLocaleString();
      } catch (_) {
        return String(v);
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
