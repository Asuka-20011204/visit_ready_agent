# Linux 一键部署

适用于 x86_64/ARM64 Linux 上的 Docker 镜像部署。脚本不需要源码、Go、Node.js 或 Git。

## Ubuntu 空白主机

先从 GitHub Actions 的发布摘要取得 40 位源码提交和镜像 digest。安装脚本也按该提交下载，避免移动 tag 改变 root 将执行的内容：

```bash
SOURCE_COMMIT='发布摘要中的40位Git提交SHA'
IMAGE_DIGEST='sha256:发布摘要中的64位镜像摘要'
curl --proto '=https' --proto-redir '=https' -fsSL \
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/$SOURCE_COMMIT/scripts/bootstrap-server.sh" \
  -o bootstrap-server.sh
chmod +x bootstrap-server.sh
sudo env INSTALL_DOCKER=true \
  EXPECTED_SOURCE_COMMIT="$SOURCE_COMMIT" \
  EXPECTED_IMAGE_DIGEST="$IMAGE_DIGEST" \
  REGISTRY_IMAGE=docker.io/asuka20011204/visit-ready-agent \
  ./bootstrap-server.sh 1.0.2
```

`INSTALL_DOCKER=true` 会通过系统包安装 Docker Engine 与 Compose v2，目前仅支持已验证的 Ubuntu。Debian 和其他发行版先按 Docker 官方文档安装，再去掉该参数运行；部署、更新和回退逻辑本身不限制发行版。

首次 live 部署会隐藏询问 `LLM_API_KEY`，并询问模型端点和模型名；MySQL 与加密密钥自动生成。文件保存在 `/opt/visit-ready`，浏览器入口由反向代理转发到 `127.0.0.1:8097`。

## 离线 Demo 主机

```bash
sudo env APP_MODE=demo \
  REGISTRY_IMAGE=docker.io/asuka20011204/visit-ready-agent \
  ./bootstrap-server.sh 1.0.2
```

Demo 和 live 必须使用不同的 `INSTALL_DIR`；已有部署不会被脚本静默切换模式。

## 私有镜像仓库

以运行脚本的同一身份登录。脚本使用 `sudo` 时也必须用 root 的 Docker 凭据：

```bash
printf '%s' "$DOCKERHUB_READ_TOKEN" | sudo docker login docker.io \
  -u YOUR_DOCKERHUB_USERNAME --password-stdin
```

只授予只读权限，并避免把令牌写入命令历史。

## 更新和手动回退

```bash
sudo env EXPECTED_SOURCE_COMMIT='1.1.0对应的40位提交SHA' \
  EXPECTED_IMAGE_DIGEST='sha256:1.1.0对应的64位摘要' \
  REGISTRY_IMAGE=docker.io/asuka20011204/visit-ready-agent \
  ./bootstrap-server.sh 1.1.0
```

脚本会锁定部署目录，拉取镜像，更新前启动旧 MySQL 并创建带 SHA-256 校验的逻辑备份，再重建应用。新版本 45 秒内未就绪时会尝试恢复上一应用版本；若旧本地标签已被清理，则按上次成功部署保存的 digest 拉取并核对源码提交。

手动回退需要提供旧版本的版本号、提交和 digest：

```bash
sudo env EXPECTED_IMAGE_DIGEST='sha256:旧版本的64位摘要' \
  EXPECTED_SOURCE_COMMIT='旧版本对应的40位Git提交SHA' \
  REGISTRY_IMAGE=docker.io/asuka20011204/visit-ready-agent \
  ./bootstrap-server.sh 1.0.2
```

一键脚本会核对镜像 OCI revision，并从该精确提交下载 Compose。公网 live 模式强制同时提供 SHA 与 digest；离线 demo 可省略。

## 公网入口

Compose 只绑定 loopback。使用 Caddy/Nginx/云负载均衡器终止 HTTPS并转发至 `http://127.0.0.1:8097`；保持 `.env` 中 `AUTH_COOKIE_SECURE=true`。不要直接把应用或 MySQL 端口开放到公网。
