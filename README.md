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
- 基础运维接口：
  - `GET /healthz`
  - `GET /metrics`

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
    upstream_base_url: "https://api.openai.com"
    upstream_api_key: "${OPENAI_API_KEY}"

  - name: "glm-prod"
    type: "openai"
    base_path: "/glm"
    upstream_base_url: "https://open.bigmodel.cn/api/paas"
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
- Prometheus 指标导出
