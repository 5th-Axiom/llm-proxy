# deploy/

Optional Prometheus + Grafana stack for observing a running llm-proxy.

## 文件

```
deploy/
├── docker-compose.yml                     Prometheus + Grafana 容器
├── prometheus/
│   ├── prometheus.yml                     scrape 配置
│   └── scrape-token.example               token 模板（复制为 scrape-token）
└── grafana/
    ├── provisioning/
    │   ├── datasources/prometheus.yml     自动注册 Prometheus 数据源
    │   └── dashboards/dashboards.yml      自动载入下面的 dashboards
    └── dashboards/
        └── llm-proxy.json                 开箱 dashboard
```

## 启动步骤

1. 在 llm-proxy 的 `config.yaml` 里启用 scrape token：

   ```yaml
   admin:
     password_hash: "..."                     # 可选：开启 UI 登录
     metrics_bearer_token: "生成一个随机字符串"   # Prometheus scrape 用
   ```

   没开 UI 登录（`password_hash` 空）的话，`metrics_bearer_token` 也可以不填，
   此时 `/metrics` 是裸开放的。

2. 把 token 写到 Prometheus 能读的文件：

   ```bash
   cd deploy/prometheus
   echo 'your-metrics-bearer-token' > scrape-token
   chmod 600 scrape-token
   ```

   （`.gitignore` 已经排除 `scrape-token`，不会误提交。）

3. 启动整个栈：

   ```bash
   cd deploy
   docker compose up -d
   ```

4. 访问：
   - Prometheus：<http://127.0.0.1:9090>，`Status → Targets` 应显示 `llm-proxy` UP
   - Grafana：<http://127.0.0.1:3000>，默认开启 anonymous viewer，能直接看
     `llm-proxy` 文件夹下的 dashboard；想登录管理则用
     `admin / ${GF_ADMIN_PASSWORD:-admin}`

## 容器如何找到 llm-proxy

Prometheus 容器用 `host.docker.internal:8081` 访问宿主机上运行的 llm-proxy。

- **Docker Desktop (macOS / Windows)**：原生支持，开箱即用。
- **Linux**：`docker-compose.yml` 里已经加了
  `extra_hosts: ["host.docker.internal:host-gateway"]`，需要 Docker 20.10+。

如果你把 llm-proxy 也容器化了，把 `targets` 改成对应的容器名即可（并确保
两个 compose 栈共享同一个网络）。

## dashboard 展示的指标

- 5m 请求速率、总请求数
- prompt / completion tokens 累计
- 按 status 拆分的请求速率时序
- 按 provider 拆分的 token 速率时序（prompt + completion 两条线）
- 按 provider 拆分的 token 累计表

基于 llm-proxy `/metrics?format=prometheus` 导出的这些 counter：

| 指标 | 标签 |
|---|---|
| `llm_proxy_requests_total` | — |
| `llm_proxy_responses_total` | `status` |
| `llm_proxy_prompt_tokens_total` | `provider` |
| `llm_proxy_completion_tokens_total` | `provider` |

想加新图直接编辑 `grafana/dashboards/llm-proxy.json` 或在 Grafana UI 里存
到 `llm-proxy` 文件夹（Grafana 的 file provider 会自动 reconcile）。
