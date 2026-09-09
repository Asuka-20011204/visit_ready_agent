# 部署配置参考

配置事实源是仓库根目录 `.env.example`、`compose.yaml` 和 `compose.mysql.yaml`。一键脚本首次运行会创建私有 `.env`；之后更新不会覆盖模型或数据密钥。

## 必填组合

### Demo

```dotenv
APP_MODE=demo
APP_PORT=8097
AUTH_MODE=disabled
SESSION_STORE=memory
```

### Live + 项目内 MySQL

```dotenv
APP_MODE=live
APP_PORT=8097
AUTH_MODE=required
AUTH_COOKIE_SECURE=false
LLM_ENDPOINT=https://供应商/v1/chat/completions
LLM_API_KEY=模型密钥
LLM_MODEL=模型名
MYSQL_PASSWORD=随机强密码
MYSQL_DSN=
SESSION_STORE=mysql
SESSION_ENCRYPTION_KEY=32字节Base64URL值
```

`AUTH_COOKIE_SECURE=false` 仅适用于本机 HTTP；公网 HTTPS 必须改为 `true`。项目内 MySQL 由 `compose.mysql.yaml` 注入 DSN，所以 `MYSQL_DSN` 必须留空。

## 完整变量表

<!-- AUTO-GENERATED: source=.env.example,compose.yaml,compose.mysql.yaml -->
| 变量 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `APP_MODE` | 否 | `demo` | `demo` 或 `live` |
| `APP_PORT` | 否 | `8097` | 宿主机 loopback 端口 |
| `APP_VERSION` | 发布时 | `dev` | 镜像与运行版本 |
| `APP_COMMIT` | 否 | `local` | 构建提交标识 |
| `AUTH_MODE` | live 自动要求 | 按模式决定 | live 必须为 `required` |
| `AUTH_COOKIE_SECURE` | 公网必填 | 按模式决定 | HTTPS 公网为 `true` |
| `LLM_ENDPOINT` | live 必填 | 无 | 完整 HTTPS Chat Completions URL |
| `LLM_API_KEY` | live 必填 | 无 | 模型供应商密钥 |
| `LLM_MODEL` | live 必填 | 无 | 供应商模型 ID |
| `BOCHA_ENDPOINT` | 否 | 博查 Web Search URL | 通常无需修改 |
| `BOCHA_API_KEY` | 否 | 无 | 留空会关闭联网资料 |
| `SESSION_STORE` | live 为 `mysql` | `memory` | `memory` 或 `mysql` |
| `MYSQL_DSN` | 外部 MySQL 必填 | 无 | 项目内 MySQL 必须留空 |
| `MYSQL_PASSWORD` | 项目内 MySQL 必填 | 无 | 16-128 位安全字符 |
| `SESSION_ENCRYPTION_KEY` | MySQL 必填 | 无 | 32 随机字节的 Base64/Base64URL 编码 |
| `SESSION_TTL` | 否 | `24h` | 会话保留时间 |
| `SESSION_CLEANUP_INTERVAL` | 否 | `1m` | 过期清理周期 |
| `UPSTREAM_TIMEOUT` | 否 | `25s` | 单次模型/搜索请求上限 |
| `WORKFLOW_TIMEOUT` | 否 | `45s` | 完整 Agent 工作流上限 |
| `LOG_LEVEL` | 否 | `info` | `debug`、`info`、`warn`、`error` |
| `HTTP_READ_HEADER_TIMEOUT` | 否 | `5s` | 请求头读取上限 |
| `HTTP_READ_TIMEOUT` | 否 | `15s` | 请求读取上限 |
| `HTTP_WRITE_TIMEOUT` | 否 | `2m` | 响应写入上限 |
| `HTTP_IDLE_TIMEOUT` | 否 | `60s` | Keep-Alive 空闲上限 |
| `HTTP_SHUTDOWN_TIMEOUT` | 否 | `2m` | 优雅退出上限 |
<!-- /AUTO-GENERATED -->

## 密钥规则

- `.env` 权限应仅允许部署用户读取；Linux 一键脚本强制为 `600`。
- `MYSQL_PASSWORD` 不应复用个人密码。脚本生成的密码只包含 Base64URL 安全字符，可直接用于 Compose DSN。
- `SESSION_ENCRYPTION_KEY` 丢失后，已有加密会话无法恢复。应离线备份 `.env` 或存入 Secret Manager。
- 不要把密钥作为命令行参数、GitHub Repository variable 或 Kubernetes ConfigMap；使用 Secret。
