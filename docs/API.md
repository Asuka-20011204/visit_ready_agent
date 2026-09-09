# HTTP API 契约

普通 REST API 返回 `application/json`；Markdown 导出返回 `text/markdown`；创建、补充和重试在请求 `Accept: text/event-stream` 时返回 SSE。所有响应均通过 `Cache-Control: no-store` 禁止浏览器和代理缓存。

## 成功包络

会话接口统一返回下列结构，`data` 是当前会话的公开视图，不包含完整原始输入、完整补充回答、内部提示词或隐藏推理。为支持人工核对，响应会包含通过证据校验的最短原文片段。

公开 live 服务使用邮箱账户登录。登录成功后服务设置 `HttpOnly`、`Secure`、`SameSite=Lax` 的 `visitready_session` Cookie；每个会话都绑定账户所有者，跨账户访问已有 ID 统一返回 `404 session_not_found`。未登录访问会话接口返回 `401 account_required`。离线 demo 才使用 `X-VisitReady-Device-Key` 的匿名设备密钥。所有状态变更请求都会校验 `Origin`，跨站请求返回 `403 cross_site_request`。

## 账户接口

- `POST /api/v1/auth/register`：请求 `{ "email": "user@example.com", "password": "至少 12 位" }`，创建账户并设置登录 Cookie，返回 `201`。
- `POST /api/v1/auth/login`：使用同一请求结构登录，返回 `200` 并轮换浏览器登录令牌。
- `POST /api/v1/auth/logout`：撤销当前令牌并清除 Cookie，返回 `204`。
- `GET /api/v1/auth/me`：返回当前账户的公开 ID 与邮箱；未登录返回 `401 account_required`。

```json
{
  "success": true,
  "data": {
    "id": "随机会话标识",
    "status": "waiting_review",
    "emergency_message": "仅在 emergency 终态返回的固定安全提醒",
    "facts": [],
    "symptom_profiles": [
      {
        "name": "症状名称",
        "source_quote": "首次描述中的原文依据",
        "evidence_quotes": ["后续追问回答中的补充依据"]
      }
    ],
    "timeline": [],
    "risk_signals": [],
    "clarification_prompts": [],
    "questions": [],
    "action_items": [],
    "sources": [],
    "events": []
  },
  "request_id": "随机请求标识"
}
```

## `POST /api/v1/sessions`

创建并运行一次 Agent 会话。

```json
{
  "input": "脱敏后的健康情况",
  "allow_web_search": true
}
```

服务会在调用模型前自动拦截姓名、电话、邮箱、证件号、详细地址等直接身份标识符。旧客户端仍可发送 `privacy_confirmed` 字段，但不再要求用户每次手动确认。

成功时返回会话当前状态。若需要补充信息，状态为 `waiting_clarification`。若确定性规则识别到用户明确报告的当前红旗表现，即使描述少于普通输入要求的 20 字，服务也不会等待模型或联网检索，而是以成功响应返回 `emergency` 终态和固定 `emergency_message`；这不是 4xx/5xx 系统错误。

创建、补充和重试请求可发送 `Accept: text/event-stream`。服务依次发送 `connected`、带真实 `duration_ms` 的 `progress`，以及包含 `{status, body}` 的最终 `result`；客户端必须以 `result.status` 判断业务成功，不应把流连接的 HTTP 200 当作最终业务状态。

## `GET /api/v1/sessions`

返回当前登录账户仍未过期的会话元数据，供用户在已登录的设备恢复历史。列表只包含 ID、状态、就诊目标和创建/过期时间，不返回事实、原始输入、回答或所有者摘要。

紧急请求不占用普通模型并发槽，但并非无限免限流：服务使用独立的高阈值紧急限流，并继续执行普通会话持久化频率限制。超过任一阈值时响应仍包含固定急救提醒，但不创建新会话，避免匿名请求耗尽会话 Store。

## `POST /api/v1/sessions/{id}/clarifications`

仅当会话处于 `waiting_clarification` 时，提交一轮补充回答并恢复 Agent。`waiting_review`、`completed` 等状态会返回 `409 invalid_session_state`，且不得改写已有问题、行动项或来源。会话最多进行四轮动态访谈，每轮返回一至三个按优先级排序的 `clarification_prompts`；信息充分时提前结束。单项“不清楚”会保留为 `uncertainties`，明确要求跳过剩余追问时才结束访谈；不同说法进入 `contradictions` 并优先澄清。

补充回答中的直接红旗或对安全追问的明确肯定会走确定性旁路，不等待同一会话中的慢模型请求。该旁路仍通过原子状态比较，只能把 `waiting_clarification` 转为 `emergency`，不能覆盖核对中或已完成的会话。系统只保留回答中的最小肯定依据，不会把复合问题列出的每一种症状都冒充为用户事实。若状态不允许转换或紧急状态无法保存，错误响应仍必须包含固定急救提醒。

```json
{
  "answer": "补充信息"
}
```

## `GET /api/v1/sessions/{id}`

读取当前会话。响应不包含内部提示词或隐藏推理。

## `DELETE /api/v1/sessions/{id}`

永久删除服务器中的会话状态，成功返回 `204 No Content`。该操作不可撤销；页面侧栏的“隐藏”只移除本浏览器中的入口，不会调用此接口。会话不存在或已经过期时返回 `404 session_not_found`。

## `POST /api/v1/sessions/{id}/confirm`

确认用户已经核对事实与当前结构化分析，并生成最终可导出版本。两个 SHA-256 摘要都由浏览器根据当前显示内容计算；任一内容发生变化后，旧摘要会被拒绝，用户必须重新核对。确认成功后，服务会清除继续访谈才需要的原始输入与完整补充回答，只保留已核对的结构化结果。

```json
{
  "visit_goal": "本次希望解决的问题",
  "facts_acknowledged": true,
  "facts_digest": "当前事实列表的 SHA-256 摘要",
  "insights_acknowledged": true,
  "review_digest": "当前症状画像、时间线、安全提示、信息缺口、问题、行动项和来源的 SHA-256 摘要"
}
```

## `POST /api/v1/sessions/{id}/retry`

仅当会话处于 `failed` 且 `failure.retryable` 为 `true` 时，从服务器已保存的上下文重新运行 Agent。请求不需要再次提交健康描述或补充回答，也不接受请求体。成功返回当前会话；状态已经变化、达到恢复上限或不是失败会话时返回 `409 retry_not_available`。

模型或工作流暂时失败时，创建与补充接口会先把会话原子保存为 `failed`，再返回 `503 agent_retryable_failure`。第三次处理仍失败时返回 `503 agent_recovery_exhausted` 且 `failure.retryable=false`。已有会话在恢复时遇到永久性无效输出会返回 `503 agent_processing_failed` 并保存为不可重试终态，便于用户查看或删除；首次创建即发生永久错误时不保存健康原文，返回不含 Session data 的 `502 agent_processing_failed`。响应中的 `data.failure` 只包含固定错误码、用户可读文案、是否可重试、尝试次数和失败时间，不包含供应商错误、提示词或健康原文。一次会话最多进行三次处理尝试。

```json
{
  "success": false,
  "data": {
    "id": "随机会话标识",
    "status": "failed",
    "failure": {
      "code": "agent_temporarily_unavailable",
      "message": "AI 暂时没有完成处理，本次内容已安全保留。",
      "retryable": true,
      "attempts": 1,
      "failed_at": "2026-09-08T00:00:00Z"
    }
  },
  "error": {
    "code": "agent_retryable_failure",
    "message": "AI 暂时没有完成处理，本次内容已安全保留。"
  },
  "request_id": "随机请求标识"
}
```

## `GET /api/v1/sessions/{id}/export`

下载 UTF-8 Markdown。`completed` 状态导出已核对的诊前清单；`emergency` 状态无需继续核对即可导出只包含固定提醒和触发依据的紧急安全摘要。其他状态返回 `409 confirmation_required`。

该接口返回 `text/markdown; charset=utf-8` 和附件文件名 `visit-ready.md`。

## 健康检查

- `GET /livez`：进程存活检查，不调用数据库、模型或搜索服务。成功返回 `{"status":"alive"}`。
- `GET /readyz`：流量就绪检查，在 2 秒预算内验证会话 Store 的连接与容量。连接失败返回 503 和 `session_store: unavailable`；容量已满仍返回 200，但标记 `session_store: at_capacity` 与 `accepting_new_sessions:false`，确保已有会话仍可读取、导出或删除。响应不暴露底层错误。
- `GET /healthz`：兼容入口，语义与 `/readyz` 相同。

模型或搜索供应商的短暂故障不会让 readiness 失败，避免外部抖动触发 Pod 重启；数据库不可用时 readiness 失败，但 liveness 仍可通过。

## 错误包络

```json
{
  "success": false,
  "error": {
    "code": "invalid_input",
    "message": "请输入 20 到 6000 个字符的脱敏健康描述。"
  },
  "request_id": "随机请求标识"
}
```

紧急保护相关错误码包括 `emergency_rate_limited`、`emergency_not_saved` 和 `emergency_session_unavailable`。这些响应可能是 `429` 或 `503`，但 `message` 始终以固定急救提醒开头；客户端不得用通用错误文案覆盖它。
