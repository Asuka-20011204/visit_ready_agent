# Visit Ready Agent

一个面向普通患者的中文自适应诊前访谈助手。它把脱敏的健康描述整理为症状画像、时间线、待确认信息、就医问题和行动清单；明确红旗表现会触发确定性安全提醒。系统不诊断、不推荐药物，也不会把联网资料写成患者事实。

> 本项目仅帮助准备就诊沟通，不能替代医生。若正在发生危及生命的情况，请立即联系 120 或前往急诊。演示请使用 `demo/` 中的合成数据。

## 核心能力

- Go 单体服务内嵌 Web UI，CloudWeGo Eino Graph 编排提取、核验、追问、受控检索和问题生成。
- live 模式使用 OpenAI-compatible Chat Completions；博查 Web Search 可选，并只接收去标识化查询。
- 最多四轮动态追问；事实、风险提示和时间线均保留用户原文依据。
- live 模式强制邮箱账户认证，会话绑定所有者，以 AES-256-GCM 加密后保存在 MySQL。
- SSE 返回真实节点进度；上游具备有限重试、抖动退避和短时熔断。
- 支持 Docker Compose、Docker Hub/GHCR 版本发布、Windows/Linux 一键部署与应用版本回退。

## 最快体验：离线 Demo

只需 Docker Desktop 或 Docker Engine + Compose v2：

```powershell
docker compose up --build -d
docker compose ps
```

打开 [http://127.0.0.1:8097](http://127.0.0.1:8097)。页面会明确显示“离线演示 · 未调用 AI”；该模式用于检查界面和流程，不创建账户，也不代表真实模型效果。

停止服务：

```powershell
docker compose down
```

## 完整功能：从首次部署到账号演示

1. 创建配置并填写模型信息：

```powershell
Copy-Item .env.example .env
```

```dotenv
APP_MODE=live
APP_PORT=8097
AUTH_COOKIE_SECURE=false
LLM_ENDPOINT=https://你的供应商/v1/chat/completions
LLM_API_KEY=你的模型密钥
LLM_MODEL=供应商支持的模型名
BOCHA_API_KEY=你的博查API密钥
MYSQL_PASSWORD=至少16位且只含字母数字下划线或连字符
MYSQL_DSN=
SESSION_ENCRYPTION_KEY=32字节随机值的Base64URL编码
```

使用项目自带 MySQL 时，`MYSQL_DSN` 必须留空。兼容 Windows PowerShell 5.1/7 的加密密钥生成命令：

```powershell
$bytes = New-Object byte[] 32
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes); $rng.Dispose()
[Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+','-').Replace('/','_')
```

2. 启动应用与 MySQL：

```powershell
docker compose -f compose.yaml -f compose.mysql.yaml up --build --force-recreate -d
docker compose -f compose.yaml -f compose.mysql.yaml ps
Invoke-RestMethod http://127.0.0.1:8097/readyz
```

3. 打开 [http://127.0.0.1:8097](http://127.0.0.1:8097)，选择“创建账户”，输入有效邮箱和至少 12 位密码。创建成功后浏览器会通过 `HttpOnly` Cookie 保持登录。

4. 粘贴 `demo/visit-input.txt` 中的合成病例。需要演示联网资料时打开“可信资料检索”，否则保持关闭可获得更快响应。

5. 回答一至三项动态追问；不确定的信息可以如实填写“不清楚”。随后逐项核对事实、症状画像、时间线、安全提示和医生问题。

6. 确认结构化内容与原文一致后完成会话并下载 Markdown。刷新页面或重启应用后，再用同一账户登录，可从左侧历史恢复尚未过期的会话。

7. 演示结束后，可在页面永久删除单个会话；退出账户只撤销登录令牌，不会等同于删除会话。

本机 HTTP 调试必须设 `AUTH_COOKIE_SECURE=false`；公网 HTTPS 必须设为 `true`。完整配置含义见 [配置参考](docs/deployment/configuration.md)。

## 部署与更新索引

| 场景 | 首次部署 | 后续更新/回退 |
| --- | --- | --- |
| Windows + Docker Desktop，无需 Go/Git | [Windows 一键部署](docs/deployment/windows.md) | 重跑同一脚本并指定新/旧版本 |
| Ubuntu 新服务器 | [Linux 一键部署](docs/deployment/linux.md) | 可选安装 Docker，自动健康检查和应用版本回退 |
| 其他 Linux，已安装 Docker | [Linux 一键部署](docs/deployment/linux.md) | 不启用自动安装参数 |
| 独立 `docker run` 或外部 MySQL | [独立容器](docs/deployment/standalone.md) | 拉取不可变标签后替换应用容器 |
| Kubernetes + 外部 MySQL | [Kubernetes](docs/deployment/kubernetes.md) | 更新镜像 digest 并观察 rollout |
| 开发完成后发布 Docker Hub/GHCR | [版本发布与机器更新](docs/deployment/releases.md) | Git tag 触发 CI，目标机器显式升级 |
| 日志、健康、备份、恢复、停机 | [运维手册](docs/deployment/runbook.md) | 按版本和数据库备份恢复 |

先从 [部署决策索引](docs/deployment/README.md) 选择路径。默认宿主机端口是 `8097`，容器内部端口始终为 `8080`。修改宿主机端口只需更改 `.env` 中的 `APP_PORT`。

> `v1.0.1` 是首个包含新版一键部署脚本的版本；新部署请使用 `v1.0.1` 或更高版本。`v1.0.0` 不包含这些脚本，且任何已发布标签都不应移动或覆盖。

## 本地开发

```powershell
$env:APP_MODE = "demo"
go run ./cmd/server
```

默认访问 [http://127.0.0.1:8097](http://127.0.0.1:8097)。验证命令：

```powershell
go test ./...
go vet ./...
npm ci
npm run test:e2e
docker compose config --quiet
```

## 项目结构

```text
cmd/server/             程序入口与依赖装配
internal/agent/         Eino Graph 与受限状态机
internal/llm/           OpenAI-compatible 模型客户端
internal/search/        博查检索适配器
internal/guard/         隐私、证据、来源和医疗边界
internal/httpapi/       账户、会话、SSE 与安全响应
internal/session/       内存/MySQL 加密会话存储
internal/webui/         内嵌 Web 界面
scripts/                发布、首次部署和更新脚本
docs/deployment/        分场景部署与运维文档
```

## 进一步文档

- [HTTP API](docs/API.md)
- [架构说明](docs/ARCHITECTURE.md)
- [评测方案](docs/EVALUATION.md)
- [威胁模型](docs/THREAT_MODEL.md)
- [Agent 评审与改进](docs/AGENT_REVIEW_AND_IMPROVEMENT.md)
- [UI 调研](docs/AGENT_UI_RESEARCH.md)
- [视觉设计系统](docs/UI_DESIGN_SYSTEM.md)

`.env` 已被 Git 忽略。模型、搜索、数据库和加密密钥不得写入镜像、仓库、日志或截图；任何已经暴露过的凭据都应立即撤销并轮换。
