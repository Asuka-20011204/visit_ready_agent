# Visit Ready Agent

一个面向普通患者的中文自适应诊前访谈助手。它根据用户提供的脱敏描述建立症状画像与时间线，优先追问影响就医安全和沟通价值的信息，生成可核对的安全提醒、个性化问题与就诊前行动清单，并可使用去标识化查询检索可信健康来源。系统不诊断、不推荐药物，也不会把联网资料写成患者事实。

> 医疗边界：本项目仅帮助准备就诊沟通，不能替代医生。若正在发生危及生命的情况，请立即联系 120 或前往急诊。课程演示只使用 `demo/` 中的合成数据。

## 交付能力

- Go 1.25 单体服务，内嵌 Web UI，无需 Node.js。
- CloudWeGo Eino Graph 负责编排提取、核验、追问、受控检索和问题生成。
- 症状画像覆盖起病、持续时长、频率、程度、规律、诱因、缓解因素和伴随表现；最多四轮动态访谈，每轮只问一至三个当前最有价值的问题，信息充分时提前结束。
- 红旗提示必须有用户原文依据并经过确定性规则二次筛查；待确认问题不会显示为已存在的风险事实。
- 最终产物包含时间线、事实依据、就诊问题的提问原因，以及可执行的记录/资料准备清单。
- OpenAI-compatible Chat Completions API 提供真实模型调用。
- 博查 Web Search API 只接收去标识化通用查询，结果还会经过可信域名校验。
- 离线 demo 使用浏览器生成的 256 位设备密钥；公网 live 模式使用邮箱账户、`HttpOnly` 会话 Cookie 和 AES-256-GCM 加密的 MySQL 存储，并支持永久删除。
- 创建、补充和重试通过 SSE 显示真实 Eino 节点进度与耗时；模型上游具备有限重试、抖动退避和短时熔断。
- Docker Compose 是答辩主路径；Kubernetes 清单用于展示生产部署思路。

## 项目结构

```text
cmd/server/             程序入口、依赖装配、超时与优雅退出
internal/agent/         Eino Graph 与受限 Agent 状态机
internal/llm/           OpenAI-compatible 模型调用和结构化响应
internal/search/        博查 Web Search API 检索适配器
internal/guard/         隐私、证据、来源和医疗越界校验
internal/httpapi/       JSON API、输入边界和安全响应头
internal/webui/         内嵌页面与静态资源
internal/session/       带 TTL 的内存/MySQL 会话存储
internal/exporter/      Markdown 导出
demo/                   合成演示输入
deploy/k8s/             Kubernetes 示例清单
```

## Docker Compose 启动

前提只有 Docker Desktop 或兼容的 Docker Engine；不需要在答辩电脑安装 Go、Node.js、模型或 Kubernetes。

### 启动方式选择

| 目标 | 启动方式 | AI 与搜索 | 会话保存 |
| --- | --- | --- | --- |
| 看界面与流程 | `docker compose up --build -d` | 离线 demo | 内存，重启清空 |
| 本机使用完整 AI | `.env` 设为 `APP_MODE=live` 并叠加 `compose.mysql.yaml` | 真实 LLM；博查可选 | 账户绑定的加密 MySQL |
| 完整 AI + 历史恢复 | 注册/登录账户后使用同一套 MySQL Compose | 真实 LLM；博查可选 | 加密 MySQL，跨应用重启保留至过期 |
| 无源码服务器部署 | 构建/导入镜像，再 `docker run` 或 Compose | 从服务器 `.env` 注入 | 推荐 MySQL 或托管 MySQL |

镜像只包含应用程序。模型密钥、数据库密码和会话加密密钥都在容器启动时从 `.env` 注入；MySQL 历史数据存放在数据库卷或外部数据库，不会打进镜像。

### 1. 无密钥流程演练

```powershell
docker compose up --build -d
docker compose ps
```

打开 [http://127.0.0.1:8080](http://127.0.0.1:8080)。默认 `APP_MODE=demo`，可以离线检查页面、追问、核对和导出流程，但此模式不会调用真实 AI，不能作为课程要求中的正式演示。

如果宿主机 `8080` 已被占用，启动前设置端口：

```powershell
$env:APP_PORT = "8097"
docker compose up --build -d
```

然后访问 [http://127.0.0.1:8097](http://127.0.0.1:8097)。

### 2. 真实 AI 与联网检索

先创建本地配置：

```powershell
Copy-Item .env.example .env
```

编辑 `.env`，至少填写：

```dotenv
APP_MODE=live
AUTH_COOKIE_SECURE=false
LLM_ENDPOINT=https://你的供应商/v1/chat/completions
LLM_API_KEY=你的模型密钥
LLM_MODEL=供应商支持的模型名
BOCHA_API_KEY=你的博查 API 密钥
```

`LLM_ENDPOINT` 必须是 HTTPS（本机 loopback 测试地址除外），供应商需要兼容 Chat Completions 和 `response_format: {"type":"json_object"}`。`BOCHA_API_KEY` 可留空，此时可信资料检索会降级跳过，事实整理仍能完成。

live 模式必须叠加 `compose.mysql.yaml`，首次打开会要求注册或登录账户。`AUTH_COOKIE_SECURE=false` 只用于本机 `http://127.0.0.1` 调试；公网 HTTPS 必须删除该行或设为 `true`。在 Docker Compose 中，`APP_PORT` 决定本机访问端口；应用在容器内始终监听 `APP_ADDR=:8080`。

重新创建服务并检查健康状态：

```powershell
docker compose -f compose.yaml -f compose.mysql.yaml up --build --force-recreate -d
docker compose -f compose.yaml -f compose.mysql.yaml ps
Invoke-RestMethod http://127.0.0.1:8080/readyz
docker compose -f compose.yaml -f compose.mysql.yaml logs --tail 50 app
```

就绪响应应包含 `"status":"ready"` 和 `"session_store":"ok"`。`/livez` 只检查进程存活；`/healthz` 保留为 `/readyz` 的兼容入口。日志只应显示运行元数据，不应出现用户输入、模型完整输出、搜索查询或密钥。

停止服务：

```powershell
docker compose -f compose.yaml -f compose.mysql.yaml down
```

### 镜像构建、导出与服务器运行

`Dockerfile` 使用多阶段构建，最终镜像不带 Go、Node.js、源码或密钥；Web UI 已编译进 Go 程序。启动 Docker Desktop 后，在项目根目录执行：

```powershell
$tag = "visit-ready-agent:20260909"
docker build --pull --build-arg VERSION=20260909 --build-arg COMMIT=local -t $tag .
docker image inspect $tag --format '{{.RepoTags}} {{.Size}} bytes'
```

使用本地镜像运行 live 模式时，还必须连接已初始化的 MySQL。`docker run` 不会自动加入项目内的 `db` 服务，因此 `.env` 必须显式包含 `APP_MODE=live`、`SESSION_STORE=mysql`、外部 MySQL 的 `MYSQL_DSN`、`SESSION_ENCRYPTION_KEY`、`LLM_ENDPOINT`、`LLM_API_KEY` 与 `LLM_MODEL`。密钥只通过 `.env` 注入，不会进入镜像层：

```powershell
docker run --rm --name visit-ready-agent `
  --env-file .env `
  -e APP_ADDR=:8080 `
  -p 127.0.0.1:8080:8080 `
  $tag
```

如果目标服务器不能访问镜像仓库，可以导出后传输：

```powershell
docker save -o visit-ready-agent-20260909.tar $tag
```

服务器上执行：

```bash
docker load -i visit-ready-agent-20260909.tar
docker run -d --name visit-ready-agent --restart unless-stopped \
  --env-file /opt/visit-ready/.env -e APP_ADDR=:8080 \
  -p 127.0.0.1:8080:8080 visit-ready-agent:20260909
```

镜像只负责应用程序；MySQL 数据必须单独使用命名卷或托管数据库保存。需要 MySQL 时，推荐在服务器上把本项目的 `compose.yaml` 与 `compose.mysql.yaml` 一起使用，而不是把数据库目录塞进应用镜像。

如果使用私有镜像仓库，构建后标记并推送：

```powershell
docker tag $tag ghcr.io/your-org/visit-ready-agent:20260909
docker push ghcr.io/your-org/visit-ready-agent:20260909
```

服务器登录仓库后，用同一个不可变标签 `docker pull`，再按上面的 `docker run` 或 Compose 配置启动。公网部署仍应通过 Caddy/Nginx 提供 HTTPS，应用端口只绑定 `127.0.0.1`。

### 自动发布与更新

仓库已经包含 CI/CD：`.github/workflows/ci.yml` 在推送到 `main` 或创建 Pull Request 时执行测试、竞态检测、覆盖率、Docker 构建和浏览器 E2E；`.github/workflows/release.yml` 在推送 `v*` 标签时重复质量门禁，并将不可变版本镜像发布到 GitHub Container Registry。它也支持可选的 Docker Hub 发布。它不会自动登录你的服务器或读取服务器 `.env`。

首次使用 GHCR 时，在本机登录并确保仓库允许 GitHub Actions 写入 Packages：

```powershell
docker login ghcr.io
git add .
git commit -m "release: prepare public deployment"
git push origin main
git tag v1.0.0
git push origin v1.0.0
```

标签工作流成功后，镜像地址为 `ghcr.io/asuka-20011204/visit-ready-agent:1.0.0`。服务器准备好 `/opt/visit-ready/.env`、`compose.yaml`、`compose.mysql.yaml` 和 `scripts/deploy-server.sh` 后，登录 GHCR 并执行：

```bash
echo "$GHCR_READ_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
REGISTRY_IMAGE=ghcr.io/asuka-20011204/visit-ready-agent \
  bash scripts/deploy-server.sh 1.0.0
```

如果使用 Docker Hub 个人仓库，例如用户名为 `yourname`、仓库为 `visit-ready-agent`，本机手动发布：

```powershell
docker login
.\scripts\publish-release.ps1 `
  -Version 1.0.0 `
  -RegistryImage docker.io/yourname/visit-ready-agent `
  -Push
```

要让 GitHub Actions 自动发布到 Docker Hub，在仓库 `Settings -> Secrets and variables -> Actions` 中新增 Secret：`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`；再新增 Repository variable `DOCKERHUB_PUBLISH_ENABLED=true`。`DOCKERHUB_TOKEN` 应使用 Docker Hub 的 Access Token，不要使用账户登录密码。之后推送 `v1.0.0` 标签，工作流会同时发布 `docker.io/yourname/visit-ready-agent:1.0.0` 和对应的提交 SHA 标签。

服务器脚本只更新指定版本，不使用 `latest`，会先拉取镜像，再通过 MySQL Compose 强制替换应用，最后检查 `/readyz`。要实现无人值守更新，可由服务器的定时任务或发布平台在 GitHub Actions 成功后调用该脚本；不要把生产 SSH 私钥或数据库密钥写入镜像或仓库。

不使用 GitHub Actions 时，可在 Windows 本机执行：

```powershell
.\scripts\publish-release.ps1 -Version 1.0.0 -Push
```

这会运行 Go 测试、`go vet`、Compose 配置检查并构建镜像；`-Push` 才会推送 GHCR。加上 `-DeployLocal` 会用该版本重建本机 MySQL Compose。`-SkipChecks` 只适合已由 CI 验证过的重复发布。

### 3. 启用 MySQL 往期会话

在本地 `.env` 中填写一个仅用于此环境的强密码和会话加密密钥：

```dotenv
MYSQL_PASSWORD=请替换为随机强密码
MYSQL_DSN=
SESSION_ENCRYPTION_KEY=32字节随机值的base64url编码
AUTH_COOKIE_SECURE=false
```

使用项目自带的 `compose.mysql.yaml` 时，`MYSQL_DSN` 必须保持为空。Compose 会自动生成容器内部连接地址：`visitready:密码@tcp(db:3306)/visitready?...`，其中 `db` 是 Compose 服务名，不能写成 `localhost`。

可用下列兼容 Windows PowerShell 5.1 和 PowerShell 7 的命令生成加密密钥：

```powershell
$bytes = New-Object byte[] 32
[System.Security.Cryptography.RNGCryptoServiceProvider]::Create().GetBytes($bytes)
[Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+','-').Replace('/','_')
```

然后使用 MySQL 覆盖文件启动。数据库端口不会暴露到宿主机，健康信息保存在命名卷中：

```powershell
docker compose -f compose.yaml -f compose.mysql.yaml up --build -d
docker compose -f compose.yaml -f compose.mysql.yaml ps
```

检查服务：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/livez
Invoke-RestMethod http://127.0.0.1:8080/readyz
docker compose -f compose.yaml -f compose.mysql.yaml logs --tail 80 app
```

停用时使用同一组 `-f` 参数。普通 `down` 会保留命名卷；只有显式执行 `down -v` 才会删除数据库数据。

### 使用已有 MySQL（不启动项目内的 db 容器）

连接已安装或托管的 MySQL 时，应用需要完整 DSN。示例中的用户名、密码和库名必须与你实际创建的 MySQL 用户一致：

```dotenv
SESSION_STORE=mysql
MYSQL_PASSWORD=与下方DSN相同的密码
MYSQL_DSN=visitready:你的密码@tcp(host.docker.internal:3306)/visitready?parseTime=true&charset=utf8mb4&loc=UTC
SESSION_ENCRYPTION_KEY=32字节随机值的Base64URL编码
```

Windows Docker Desktop 访问宿主机 MySQL 时使用 `host.docker.internal`。Linux 服务器上该名称通常不可用：MySQL 在同一 Docker 网络时使用服务名，例如 `db:3306`；MySQL 在另一台服务器时使用私网主机名或 IP。不要将 MySQL 的 `3306` 暴露到公网。

数据库必须已有 `visitready` 库和一个非 root 用户；应用启动时会自动创建和迁移会话表。创建数据库和用户的示例：

```sql
CREATE DATABASE visitready CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'visitready'@'%' IDENTIFIED BY '你的密码';
GRANT ALL PRIVILEGES ON visitready.* TO 'visitready'@'%';
FLUSH PRIVILEGES;
```

## 本机 Go 启动（开发用）

```powershell
$env:APP_MODE = "demo"
go run ./cmd/server
```

真实调用使用与 `.env.example` 相同的环境变量。直接运行时默认只监听 `127.0.0.1:8080`；容器显式监听 `:8080`。

## Windows 单文件交付

在已安装 Go 的构建机执行以下命令，生成 `dist\\visit-ready.exe` 与 SHA-256 校验值。运行该文件仍需要用环境变量提供 live 模式的模型、MySQL 和会话加密配置：

```powershell
.\scripts\build-windows.ps1 -Version 1.0.0 -Commit local
```

## 环境变量

| 变量                         | 默认值                                    | 用途                                              |
| ---------------------------- | ----------------------------------------- | ------------------------------------------------- |
| `APP_MODE`                 | `demo`                                  | `demo` 为确定性假实现，`live` 才会调用真实 AI |
| `AUTH_MODE`                | demo 为 `disabled`；live 为 `required` | live 固定要求账户认证，不能关闭 |
| `AUTH_COOKIE_SECURE`       | demo 为 `false`；live 为 `true`         | 公网 HTTPS 必须为 `true`；本机 HTTP 联调才设 `false` |
| `APP_ADDR`                 | `127.0.0.1:8080`                       | HTTP 监听地址；容器内显式设为 `:8080`             |
| `LLM_ENDPOINT`             | 无                                        | live 模式必填的 Chat Completions 完整地址         |
| `LLM_API_KEY`              | 无                                        | live 模式必填，只通过环境或 Secret 注入           |
| `LLM_MODEL`                | 无                                        | live 模式必填的模型标识                           |
| `BOCHA_ENDPOINT`           | `https://api.bochaai.com/v1/web-search` | 博查 Web Search API 地址                          |
| `BOCHA_API_KEY`            | 无                                        | 可选；缺失时跳过联网搜索                          |
| `SESSION_STORE`            | `memory`                                | `memory` 或 `mysql`                               |
| `MYSQL_DSN`                | 无                                      | 外部/托管 MySQL 模式必填；使用 `compose.mysql.yaml` 时留空 |
| `MYSQL_PASSWORD`           | 无                                      | 项目内 MySQL Compose 的数据库用户密码；外部 MySQL 时仅作与 DSN 对照，应用不读取它 |
| `SESSION_ENCRYPTION_KEY`   | 无                                      | MySQL 模式必填；32 字节随机值的 Base64/Base64URL 编码 |
| `SESSION_TTL`              | `24h`                                   | 会话保留时间                                      |
| `SESSION_CLEANUP_INTERVAL` | `1m`                                    | 过期会话清理周期                                  |
| `UPSTREAM_TIMEOUT`         | `25s`                                   | 单次模型或搜索调用超时                            |
| `WORKFLOW_TIMEOUT`         | `45s`                                   | 单次完整 Agent 工作流的总时间预算，不能超过 45 秒；Docker Compose 固定为 `45s`，直接 Go 启动才会读取此变量 |
| `HTTP_READ_HEADER_TIMEOUT` | `5s`                                    | HTTP 请求头读取超时                               |
| `HTTP_READ_TIMEOUT`        | `15s`                                   | HTTP 请求读取超时                                 |
| `HTTP_WRITE_TIMEOUT`       | `2m`                                    | HTTP 响应写入超时                                 |
| `HTTP_IDLE_TIMEOUT`        | `60s`                                   | HTTP keep-alive 空闲超时                          |
| `HTTP_SHUTDOWN_TIMEOUT`    | `2m`                                    | 优雅退出等待时间                                  |

`.env` 已被 Git 忽略。不要把密钥写进镜像、Compose、Kubernetes ConfigMap、演示数据或日志；任何曾经出现在聊天、截图或明文配置中的密钥都应在供应商控制台立即轮换。

## 8 分钟现场演示顺序

1. 用 `docker compose ps` 展示容器为 healthy，并打开首页确认顶部显示“真实 AI 模式”。
2. 粘贴 `demo/visit-input.txt`，按需开启联网检索并提交；身份信息会在发送前自动拦截。
3. 展示 Agent 从原文提取的事实及逐条原文依据。
4. 若进入追问状态，优先回答安全问题和症状时间/频率问题；也可如实回答“不确定”、跳过单项或主动补充其他信息。Agent 会保留未知与冲突，不会将模糊回答写成确定事实。
5. 展示症状画像、时间线和风险提示如何与原文依据绑定；强调风险提示不是诊断。
6. 展示可信来源的标题、域名和链接，强调查询已去标识化且搜索失败可以降级。
7. 展示带“为什么问”的医生问题和个性化行动清单，逐项核对事实，再单独确认结构化分析与原文一致。
8. 确认后下载 Markdown，现场打开文件。
9. 打开 `cmd/server/main.go`、`internal/agent/workflow.go` 和 `internal/llm/openai.go`：分别讲程序入口、Eino 编排、真实 AI HTTP 调用及结果进入会话的位置。

答辩前录制一次相同流程作为断网兜底，但现场仍应先运行真实 live 模式。不要现场注册账号、安装环境或下载依赖。

## Kubernetes 示例

Kubernetes 是可选展示路径。镜像必须先推送到可访问的镜像仓库，并把 `deploy/k8s/deployment.yaml` 中的 `registry.example.com/visit-ready-agent:1.0.0` 替换为实际不可变版本标签或 digest。

先创建命名空间和 Secret，再应用其余清单：

```powershell
kubectl apply -f deploy/k8s/namespace.yaml
kubectl -n visit-ready create secret generic visit-ready-secrets `
  --from-literal=LLM_API_KEY='你的模型密钥' `
  --from-literal=BOCHA_API_KEY='你的博查 API 密钥' `
  --from-literal=MYSQL_DSN='visitready:密码@tcp(mysql.internal:3306)/visitready?parseTime=true&charset=utf8mb4&loc=UTC' `
  --from-literal=SESSION_ENCRYPTION_KEY='32字节随机值的Base64URL编码'
kubectl apply -k deploy/k8s
kubectl -n visit-ready rollout status deployment/visit-ready-agent --timeout=120s
kubectl -n visit-ready port-forward service/visit-ready-agent 8080:80
```

随后访问 [http://127.0.0.1:8080](http://127.0.0.1:8080)。示例启用了非 root、只读根文件系统、Linux capabilities 全部移除、默认 seccomp、资源限制和三类健康探针。

Kubernetes live 示例使用外部 MySQL 保存账户与加密会话；在执行命令前必须先创建 `visitready` 数据库和非 root 用户。当前仍固定为 1 个副本和 `Recreate`，因为限流与并发槽尚未共享；扩容前需要提供 Redis 等共享限流后端。

## 验证

```powershell
go test ./...
go test -race ./...
go vet ./...
npm ci
npx playwright install chromium
npm run test:e2e
docker compose config --quiet
docker build -t visit-ready-agent:local .
```

Docker 启动后再验证：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/readyz
docker inspect --format '{{.State.Health.Status}}' visit-ready-agent-app-1
```

容器名可能因目录名不同而变化，优先以 `docker compose ps` 为准。安全和验收设计详见 `docs/THREAT_MODEL.md` 与 `docs/EVALUATION.md`。Agent 交互调研与可持续的视觉规范分别记录在 `docs/AGENT_UI_RESEARCH.md` 和 `docs/UI_DESIGN_SYSTEM.md`。

## 常见问题

- **live 模式启动失败**：检查三个 `LLM_*` 变量是否非空，端点是否为完整 HTTPS Chat Completions 地址。
- **模型返回非法 JSON**：确认模型支持 JSON object 响应格式；服务会拒绝不符合 Schema 的返回，不会拼凑结果。
- **搜索没有资料**：确认 `BOCHA_API_KEY` 有效；搜索失败按设计降级，不影响患者事实整理。
- **端口被占用**：在 `.env` 设置 `APP_PORT=8081`，再访问 `http://127.0.0.1:8081`。
- **容器构建下载超时**：在网络稳定时提前执行 `docker compose build`；答辩当天不要临时拉取基础镜像或模块。
- **会话突然消失**：内存模式在服务重启后清空；MySQL 模式可跨重启恢复，但仍会在默认 24 小时后自动过期。
