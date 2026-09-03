# HTTP API 契约

除 Markdown 导出外，所有 API 返回 `application/json`，并通过 `Cache-Control: no-store` 禁止浏览器和代理缓存。

## 成功包络

会话接口统一返回下列结构，`data` 是当前会话的公开视图，不包含原始输入、补充原文、内部提示词或隐藏推理。

```json
{
  "success": true,
  "data": {
    "id": "随机会话标识",
    "status": "waiting_review",
    "facts": [],
    "questions": [],
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
  "allow_web_search": true,
  "privacy_confirmed": true
}
```

成功时返回会话当前状态。若需要补充信息，状态为 `waiting_clarification`。

## `POST /api/v1/sessions/{id}/clarifications`

提交一轮补充回答并恢复 Agent。

```json
{
  "answer": "补充信息"
}
```

## `GET /api/v1/sessions/{id}`

读取当前会话。响应不包含内部提示词或隐藏推理。

## `POST /api/v1/sessions/{id}/confirm`

确认用户已经核对事实，并生成最终可导出版本。

```json
{
  "visit_goal": "本次希望解决的问题"
}
```

## `GET /api/v1/sessions/{id}/export`

下载 UTF-8 Markdown。只有 `completed` 状态允许导出。

该接口返回 `text/markdown; charset=utf-8` 和附件文件名 `visit-ready.md`。

## `GET /healthz`

进程存活检查，不调用外部服务，不泄漏配置。

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
