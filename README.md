# llm-proxy

一个用 Go 实现的高性能 LLM 代理服务。

它的目标很简单：

- 客户端只配置代理地址和代理自己的 token
- 上游厂商 token 只保存在服务端配置文件里
- 通过配置文件注册多个 provider
- `type` 只分 `openai` 和 `anthropic`
- 以最小转发开销支持普通请求和 stream 直通

## 当前能力

- `openai` 类型：
  - `POST /<base_path>/v1/chat/completions`
  - `GET /<base_path>/v1/models`
- `anthropic` 类型：
  - `POST /<base_path>/v1/messages`
- 多个 provider 实例可复用同一个协议适配器
  - 例如 GLM 可配置为 `type: openai`
- 代理鉴权支持：
  - `Authorization: Bearer <proxy-token>`
  - `x-api-key: <proxy-token>`
  - 多租户：每个 user 由 admin 建、各自持有独立 key（`llmp_*`）；token 存 SHA-256 到 SQLite；revoke 即时生效
  - 按 user + provider 落 `usage_records` 表（支持 per-user token 用量查询），超 `storage.usage_retention_days`（默认 90d）自动清理
- 基础运维接口：
  - `GET /healthz`（与代理共用监听地址）
  - `GET /metrics`（默认监听 `127.0.0.1:8081`，避免在公网暴露 provider 名与 token 计数）
- 非流式请求默认 60s 上游超时；`Accept: text/event-stream` 或请求体含 `"stream": true` 时不加超时
- Admin 管理界面（与 `/metrics` 共用 loopback 监听）：
  - Web UI：`http://127.0.0.1:8081/ui/`
    - Providers 页：增删改 provider，改完立即热生效，不重启进程
    - Metrics 页：请求数 / 状态分布 / 按 provider 的 token 使用，3s 自动刷新
  - REST API：`GET/POST/PUT/DELETE /api/providers[/:name]`、`GET/POST /api/users`、`GET/PATCH/DELETE /api/users/:id`、`GET/POST /api/users/:id/keys`、`DELETE /api/keys/:prefix`、`GET /api/usage/summary?window=7d`、`GET /api/config`、`GET /metrics`
  - 可选密码登录：在 `admin.password_hash` 填入 `./llm-proxy -hash-password` 生成的哈希，开启 session cookie 鉴权；未设置则保持开放（适合纯 loopback 场景）
  - 写回 YAML 时保留 `${ENV_VAR}` 占位符，不会把上游 key 明文落盘
- Prometheus 外挂：
  - 配置 `admin.metrics_bearer_token` 后，scraper 可用 `Authorization: Bearer <token>` 绕开 session cookie 抓 `/metrics?format=prometheus`
  - `deploy/docker-compose.yml` 提供一套 Prometheus + Grafana（含预置 dashboard）的 loopback 栈，详见 `deploy/README.md`

## 设计原则

- 使用 Go 标准库 `net/http`
- 请求体默认不做 JSON 反序列化
- stream 按字节流透传，不做事件重组
- 连接池由共享 `http.Transport` 管理
- 默认不记录 prompt 和 response 正文，避免泄漏敏感内容

## 配置

参考 `config.example.yaml`：

```yaml
server:
  listen: ":8080"
  tokens:
    - "proxy-token-1"

providers:
  - name: "openai-main"
    type: "openai"
    base_path: "/openai"
    upstream_base_url: "https://api.openai.com/v1"
    upstream_api_key: "${OPENAI_API_KEY}"

  - name: "glm-prod"
    type: "openai"
    base_path: "/glm"
    upstream_base_url: "https://open.bigmodel.cn/api/coding/paas/v4"
    upstream_api_key: "${GLM_API_KEY}"

  - name: "claude-main"
    type: "anthropic"
    base_path: "/anthropic"
    upstream_base_url: "https://api.anthropic.com"
    upstream_api_key: "${ANTHROPIC_API_KEY}"
    upstream_headers:
      anthropic-version: "2023-06-01"
```

## 启动

```bash
go run ./cmd/llm-proxy -config config.example.yaml
```

`openai` 类型的 `upstream_base_url` 可以直接写厂商要求的完整 base URL。
如果这个 URL 已经包含版本段，比如 `https://api.openai.com/v1` 或 `https://open.bigmodel.cn/api/coding/paas/v4`，代理不会再重复拼一个 `/v1`。

## 调用示例

OpenAI 兼容请求：

```bash
curl "http://127.0.0.1:8080/openai/v1/chat/completions" \
  -H "Authorization: Bearer proxy-token-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4.1-mini",
    "messages": [{"role":"user","content":"hello"}],
    "stream": true
  }'
```

GLM 兼容请求：

```bash
curl "http://127.0.0.1:8080/glm/v1/chat/completions" \
  -H "Authorization: Bearer proxy-token-1" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-4-flash",
    "messages": [{"role":"user","content":"hello"}]
  }'
```

Anthropic 请求：

```bash
curl "http://127.0.0.1:8080/anthropic/v1/messages" \
  -H "x-api-key: proxy-token-1" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{
    "model": "claude-sonnet-4-5",
    "max_tokens": 256,
    "messages": [{"role":"user","content":"hello"}]
  }'
```

## 开发验证

运行测试：

```bash
go test ./...
```

运行基准：

```bash
go test ./internal/server -run '^$' -bench . -benchmem
```

## 后续可以扩展的方向

- provider 级别的访问白名单
- 动态配置热更新
- 更完整的模型/接口覆盖
