# Visit Ready Agent

一个面向普通患者的中文诊前信息整理助手。它把用户提供的零散症状、时间变化、用药与检查信息整理成可核对摘要，最多追问一轮，并可使用去标识化查询检索可信健康来源。系统不诊断、不推荐药物，也不会把联网资料写成患者事实。

> 医疗边界：本项目仅帮助准备就诊沟通，不能替代医生。若正在发生危及生命的情况，请立即联系 120 或前往急诊。课程演示只使用 `demo/` 中的合成数据。

## 交付能力

- Go 1.25 单体服务，内嵌 Web UI，无需 Node.js。
- CloudWeGo Eino Graph 负责编排提取、核验、追问、受控检索和问题生成。
- OpenAI-compatible Chat Completions API 提供真实模型调用。
- Tavily 只接收去标识化通用查询，结果还会经过可信域名校验。
- 会话只保存在内存中，默认 30 分钟过期；确认后可导出 Markdown。
- Docker Compose 是答辩主路径；Kubernetes 清单用于展示生产部署思路。

## 项目结构

```text
cmd/server/             程序入口、依赖装配、超时与优雅退出
internal/agent/         Eino Graph 与受限 Agent 状态机
internal/llm/           OpenAI-compatible 模型调用和结构化响应
internal/search/        Tavily 检索适配器
internal/guard/         隐私、证据、来源和医疗越界校验
internal/httpapi/       JSON API、输入边界和安全响应头
internal/webui/         内嵌页面与静态资源
internal/session/       带 TTL 的内存会话
internal/exporter/      Markdown 导出
demo/                   合成演示输入
deploy/k8s/             Kubernetes 示例清单
```

## Docker Compose 启动

前提只有 Docker Desktop 或兼容的 Docker Engine；不需要在答辩电脑安装 Go、Node.js、模型或 Kubernetes。

### 1. 无密钥流程演练

```powershell
docker compose up --build -d
docker compose ps
```

打开 <http://127.0.0.1:8080>。默认 `APP_MODE=demo`，可以离线检查页面、追问、核对和导出流程，但此模式不会调用真实 AI，不能作为课程要求中的正式演示。

### 2. 真实 AI 与联网检索

先创建本地配置：

```powershell
Copy-Item .env.example .env
```

编辑 `.env`，至少填写：

```dotenv
APP_MODE=live
LLM_ENDPOINT=https://你的供应商/v1/chat/completions
LLM_API_KEY=你的模型密钥
LLM_MODEL=供应商支持的模型名
TAVILY_API_KEY=你的Tavily密钥
```

`LLM_ENDPOINT` 必须是 HTTPS（本机 loopback 测试地址除外），供应商需要兼容 Chat Completions 和 `response_format: {"type":"json_object"}`。`TAVILY_API_KEY` 可留空，此时可信资料检索会降级跳过，事实整理仍能完成。

重新创建服务并检查健康状态：

```powershell
docker compose up --build --force-recreate -d
docker compose ps
Invoke-RestMethod http://127.0.0.1:8080/healthz
docker compose logs --tail 50 app
```

健康响应应包含 `"status":"ok"`。日志只应显示运行元数据，不应出现用户输入、模型完整输出、搜索查询或密钥。

停止服务：

```powershell
docker compose down
```

## 本机 Go 启动（开发用）

```powershell
$env:APP_MODE = "demo"
go run ./cmd/server
```

真实调用使用与 `.env.example` 相同的环境变量。应用默认监听 `:8080`。

## 环境变量

| 变量 | 默认值 | 用途 |
|---|---|---|
| `APP_MODE` | `demo` | `demo` 为确定性假实现，`live` 才会调用真实 AI |
| `APP_ADDR` | `:8080` | HTTP 监听地址；容器内保持默认值 |
| `LLM_ENDPOINT` | 无 | live 模式必填的 Chat Completions 完整地址 |
| `LLM_API_KEY` | 无 | live 模式必填，只通过环境或 Secret 注入 |
| `LLM_MODEL` | 无 | live 模式必填的模型标识 |
| `TAVILY_ENDPOINT` | `https://api.tavily.com/search` | Tavily 搜索地址 |
| `TAVILY_API_KEY` | 无 | 可选；缺失时跳过联网搜索 |
| `SESSION_TTL` | `30m` | 内存会话存活时间 |
| `SESSION_CLEANUP_INTERVAL` | `1m` | 过期会话清理周期 |
| `UPSTREAM_TIMEOUT` | `25s` | 单次模型或搜索调用超时 |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | HTTP 请求头读取超时 |
| `HTTP_READ_TIMEOUT` | `15s` | HTTP 请求读取超时 |
| `HTTP_WRITE_TIMEOUT` | `60s` | HTTP 响应写入超时 |
| `HTTP_IDLE_TIMEOUT` | `60s` | HTTP keep-alive 空闲超时 |
| `HTTP_SHUTDOWN_TIMEOUT` | `55s` | 优雅退出等待时间（覆盖最长 45 秒工作流） |

`.env` 已被 Git 忽略。不要把密钥写进镜像、Compose、Kubernetes ConfigMap、演示数据或日志；任何曾经出现在聊天、截图或明文配置中的密钥都应在供应商控制台立即轮换。

## 8 分钟现场演示顺序

1. 用 `docker compose ps` 展示容器为 healthy，并打开首页确认顶部显示“真实 AI 模式”。
2. 粘贴 `demo/visit-input.txt`，勾选隐私确认与联网检索，提交。
3. 展示 Agent 从原文提取的事实及逐条原文依据。
4. 若进入追问状态，粘贴 `demo/clarification.txt`，说明系统最多追问一轮。
5. 展示可信来源的标题、域名和链接，强调查询已去标识化且搜索失败可以降级。
6. 展示“建议向医生询问”的问题，核对事实并填写本次就诊目标。
7. 确认后下载 Markdown，现场打开文件。
8. 打开 `cmd/server/main.go`、`internal/agent/workflow.go` 和 `internal/llm/openai.go`：分别讲程序入口、Eino 编排、真实 AI HTTP 调用及结果进入会话的位置。

答辩前录制一次相同流程作为断网兜底，但现场仍应先运行真实 live 模式。不要现场注册账号、安装环境或下载依赖。

## Kubernetes 示例

Kubernetes 是可选展示路径。镜像必须先推送到可访问的镜像仓库，并把 `deploy/k8s/deployment.yaml` 中的 `registry.example.com/visit-ready-agent:1.0.0` 替换为实际不可变版本标签或 digest。

先创建命名空间和 Secret，再应用其余清单：

```powershell
kubectl apply -f deploy/k8s/namespace.yaml
kubectl -n visit-ready create secret generic visit-ready-secrets `
  --from-literal=LLM_API_KEY='你的模型密钥' `
  --from-literal=TAVILY_API_KEY='你的Tavily密钥'
kubectl apply -k deploy/k8s
kubectl -n visit-ready rollout status deployment/visit-ready-agent --timeout=120s
kubectl -n visit-ready port-forward service/visit-ready-agent 8080:80
```

随后访问 <http://127.0.0.1:8080>。示例启用了非 root、只读根文件系统、Linux capabilities 全部移除、默认 seccomp、资源限制和三类健康探针。

当前会话存储在单进程内存，因此清单固定为 1 个副本并使用 `Recreate`。如果要扩容，必须先换成带过期策略的共享会话存储；滚动重启会丢失未导出的会话，这是当前明确的恢复边界。

## 验证

```powershell
go test ./...
go test -race ./...
go vet ./...
docker compose config --quiet
docker build -t visit-ready-agent:local .
```

Docker 启动后再验证：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz
docker inspect --format '{{.State.Health.Status}}' visit-ready-agent-app-1
```

容器名可能因目录名不同而变化，优先以 `docker compose ps` 为准。安全和验收设计详见 `docs/THREAT_MODEL.md` 与 `docs/EVALUATION.md`。

## 常见问题

- **live 模式启动失败**：检查三个 `LLM_*` 变量是否非空，端点是否为完整 HTTPS Chat Completions 地址。
- **模型返回非法 JSON**：确认模型支持 JSON object 响应格式；服务会拒绝不符合 Schema 的返回，不会拼凑结果。
- **搜索没有资料**：确认 `TAVILY_API_KEY` 有效；搜索失败按设计降级，不影响患者事实整理。
- **端口被占用**：在 `.env` 设置 `APP_PORT=8081`，再访问 `http://127.0.0.1:8081`。
- **容器构建下载超时**：在网络稳定时提前执行 `docker compose build`；答辩当天不要临时拉取基础镜像或模块。
- **会话突然消失**：会话默认 30 分钟过期，容器重启也会清空，这是“不落盘保存健康信息”的隐私取舍。
