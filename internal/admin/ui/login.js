"use strict";

const { createApp } = Vue;
const { ElMessage } = ElementPlus;

createApp({
  data() {
    return {
      password: "",
      loading: false,
      error: "",
    };
  },

  methods: {
    async checkExistingSession() {
      try {
        const r = await fetch("/api/auth");
        const auth = await r.json();
        if (!auth.enabled || auth.authenticated) {
          location.replace("/ui/");
        }
      } catch (_) {
        // Keep the login page usable if auth probing fails transiently.
      }
    },

    async login() {
      if (!this.password) {
        this.error = "请输入密码";
        return;
      }

      this.loading = true;
      this.error = "";
      try {
        const r = await fetch("/api/login", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ password: this.password }),
        });
        if (!r.ok) {
          this.error = r.status === 401 ? "密码错误" : (await r.text()).trim() || r.statusText;
          return;
        }
        ElMessage.success("登录成功");
        location.replace("/ui/");
      } catch (err) {
        this.error = String(err.message || err);
      } finally {
        this.loading = false;
      }
    },
  },

  mounted() {
    this.checkExistingSession();
  },
}).use(ElementPlus).mount("#app");
