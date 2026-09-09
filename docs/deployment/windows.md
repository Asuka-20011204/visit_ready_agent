# Windows 一键部署

适用于 Windows 10/11 + Docker Desktop。无需 Go、Node.js 或 Git；首次运行前启动 Docker Desktop，并确认 `docker compose version` 可用。

安装脚本从 `main` 下载；应用镜像和 Compose 仍由发布摘要中的 `SourceCommit` 与 `ImageDigest` 固定。不要用镜像构建提交去下载安装脚本，否则会拿到旧的备份逻辑。

## 获取 SourceCommit 和 ImageDigest

这两个值来自成功的镜像发布记录，不是自行生成的随机值：

- `SourceCommit`：该版本标签实际构建的 40 位 Git 提交 SHA。安装脚本用它核对镜像 OCI revision，并从同一提交下载 Compose。
- `ImageDigest`：镜像仓库为该镜像内容生成的 `sha256:` 摘要。Docker Hub 和 GHCR 的摘要可能不同，必须使用与 `RegistryImage` 相同仓库的摘要。

在 GitHub 页面打开仓库，依次进入 `Actions` -> `release-image` -> 选择成功的版本运行（例如 `v1.0.4`）-> `Summary`。页面底部有两个发布 Job：

- `publish`：GHCR 的 `Source commit` 和 `Published digest`。
- `publish-dockerhub`：Docker Hub 的 `Source commit` 和 `Published digest`。

脚本默认使用 Docker Hub，所以应复制 `publish-dockerhub` 的 digest。当前 `v1.0.4` 可直接填写：

```powershell
$SourceCommit = '70dbb69b841c9039882b5ab52f0eff6fc3605c78'
$ImageDigest = 'sha256:6a8132ecbb233061c8f11beaec569cc55422431ad68a3337ee34b4e900b494e1'
```

也可以安装 GitHub CLI 后在仓库目录查看发布日志：

```powershell
gh run view 34389723434 --log |
  Select-String 'Source commit:|Published digest:'
```

输出前缀为 `publish-dockerhub` 的摘要属于 Docker Hub；前缀为 `publish` 的摘要属于 GHCR。还可以用 `git rev-list -n 1 v1.0.4` 单独获取 `SourceCommit`，但生产部署仍应以成功发布运行显示的值为准。

## 首次 Demo

先从 `main` 下载当前安装脚本，再部署不可变的 `v1.0.4` 镜像：

```powershell
Invoke-WebRequest `
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/main/scripts/bootstrap-windows.ps1" `
  -OutFile bootstrap-windows.ps1
Set-ExecutionPolicy -Scope Process Bypass
.\bootstrap-windows.ps1 -Version 1.0.4 -Mode demo
```

打开 `http://127.0.0.1:8097`。Demo 不调用模型，不创建账户，适合先确认界面和容器环境。

## 首次 Live

本机 HTTP 调试可直接执行：

```powershell
$SourceCommit = '70dbb69b841c9039882b5ab52f0eff6fc3605c78'
$ImageDigest = 'sha256:6a8132ecbb233061c8f11beaec569cc55422431ad68a3337ee34b4e900b494e1'
Invoke-WebRequest `
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/main/scripts/bootstrap-windows.ps1" `
  -OutFile bootstrap-windows.ps1
Set-ExecutionPolicy -Scope Process Bypass
.\bootstrap-windows.ps1 -Version 1.0.4 -Mode live `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

如果已经从 `main` 下载过脚本，只需：

```powershell
$SourceCommit = '70dbb69b841c9039882b5ab52f0eff6fc3605c78'
$ImageDigest = 'sha256:6a8132ecbb233061c8f11beaec569cc55422431ad68a3337ee34b4e900b494e1'
.\bootstrap-windows.ps1 -Version 1.0.4 -Mode live `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

脚本会依次安全询问模型端点、模型密钥、模型名和可选博查密钥，自动生成 MySQL 密码与会话加密密钥。配置和 Compose 文件默认保存在 `%LOCALAPPDATA%\VisitReady`。

打开 `http://127.0.0.1:8097`，创建邮箱账户，再按仓库 README 的完整演示流程操作。本机 HTTP 使用 `AUTH_COOKIE_SECURE=false`；不要把该设置复制到公网。

首次 live **不会**备份 MySQL。只有目录里已有 `.deployment-success` 时，脚本才会等待数据库健康，并用 TCP 连接 `127.0.0.1` 做升级前 dump。不要用镜像构建提交去下载安装脚本。

## 把已有 Live 升到 1.0.4

已有 `%LOCALAPPDATA%\VisitReady` 且 `APP_MODE=live` 时，不要改 `.env`。先重新下载 `main` 上的安装脚本，再升级镜像：

```powershell
$SourceCommit = '70dbb69b841c9039882b5ab52f0eff6fc3605c78'
$ImageDigest = 'sha256:6a8132ecbb233061c8f11beaec569cc55422431ad68a3337ee34b4e900b494e1'
Invoke-WebRequest `
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/main/scripts/bootstrap-windows.ps1" `
  -OutFile bootstrap-windows.ps1
Set-ExecutionPolicy -Scope Process Bypass
.\bootstrap-windows.ps1 -Version 1.0.4 `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

如果当前目录已经是仓库根目录，也可以直接调用仓库脚本：

```powershell
$SourceCommit = '70dbb69b841c9039882b5ab52f0eff6fc3605c78'
$ImageDigest = 'sha256:6a8132ecbb233061c8f11beaec569cc55422431ad68a3337ee34b4e900b494e1'
Set-ExecutionPolicy -Scope Process Bypass
.\scripts\bootstrap-windows.ps1 -Version 1.0.4 `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

完成后检查：

```powershell
docker compose -f "$env:LOCALAPPDATA\VisitReady\compose.yaml" -f "$env:LOCALAPPDATA\VisitReady\compose.mysql.yaml" ps
Invoke-RestMethod http://127.0.0.1:8097/readyz
```

## 常见失败

如果安装停在：

```text
mysqldump: Got error: 2002: Can't connect to local MySQL server through socket '/var/run/mysqld/mysqld.sock'
```

说明当时用的是旧安装脚本：它会在首次 live 或 MySQL 未就绪时备份，并且走 Unix socket。处理方式：

1. 重新从 `main` 下载 `bootstrap-windows.ps1`，不要用某个历史 `SourceCommit` 去下载脚本。
2. 确认 `%LOCALAPPDATA%\VisitReady\.env` 仍在；不要删除会话加密密钥。
3. 再次执行同一版本的 live 命令。首次安装应直接启动 `db` 和应用；只有已成功部署过的目录才会备份。
4. 备份命令现在是 `mysqldump --protocol=TCP -h 127.0.0.1`，并先执行 `docker compose up -d --wait db`。

第二轮补充输入框被藏住，属于 `v1.0.3` 及更早应用镜像的缺陷。`v1.0.4` 已修复；只重跑 `v1.0.3` 不会带上前端修复。

## 公网 Live

从 GitHub Actions 的 `publish-dockerhub` 发布摘要复制 40 位源码提交和 `sha256:` 镜像摘要。安装脚本仍从 `main` 下载，镜像和 Compose 用发布摘要钉死。下面是可直接执行的 `v1.0.4` Docker Hub 示例：

```powershell
$SourceCommit = '70dbb69b841c9039882b5ab52f0eff6fc3605c78'
$ImageDigest = 'sha256:6a8132ecbb233061c8f11beaec569cc55422431ad68a3337ee34b4e900b494e1'
Invoke-WebRequest `
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/main/scripts/bootstrap-windows.ps1" `
  -OutFile bootstrap-windows.ps1
Set-ExecutionPolicy -Scope Process Bypass
.\bootstrap-windows.ps1 -Version 1.0.4 -Mode live `
  -AuthCookieSecure $true `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

若改用 GHCR，则必须同时指定 GHCR 地址和它自己的摘要：

```powershell
$ImageDigest = 'sha256:d0e4229c50f4f2b9b8a1178861f63b7bb7e4f3f25f3d6a0fc5b5fbf04b959743'
.\bootstrap-windows.ps1 -Version 1.0.4 -Mode live `
  -RegistryImage 'ghcr.io/asuka-20011204/visit-ready-agent' `
  -AuthCookieSecure $true `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

应用仍只绑定 `127.0.0.1:8097`。由 Caddy、Nginx 或云负载均衡器提供 HTTPS，再反向代理到该地址；不要直接开放应用或 MySQL 端口。

## 更新与回退

发布 `1.1.0` 后，重新下载 `main` 上的安装脚本再执行：

```powershell
.\bootstrap-windows.ps1 -Version 1.1.0
```

脚本保留原 `.env` 与 MySQL 卷，拉取并切换新镜像，只有 `/readyz` 成功后才写入新版本号。失败会尝试恢复上一应用版本。手动回退到上一成功版本：

```powershell
$SourceCommit = 'fcc7021129d15cdaa9df75175559a0cf233c2c67'
$ImageDigest = 'sha256:caa353cd12a2912e58f45b6d5fe3b5f2d96f906dda0840261cc9b437ebc66e1e'
.\bootstrap-windows.ps1 -Version 1.0.3 `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

应用回退不等于数据库回退。涉及不兼容迁移时，先按 [运维手册](runbook.md) 备份并验证恢复。

公网生产更新必须传入发布摘要中的 `-ExpectedSourceCommit` 与 `-ExpectedImageDigest`，把 Compose 源提交和镜像内容固定到同一发布记录。脚本会保存成功版本的 digest 和源码提交；自动回退时若旧本地标签不存在，会使用已保存的 digest 拉取并核对旧镜像。

## 自定义目录或端口

```powershell
.\bootstrap-windows.ps1 -Version 1.0.4 -Mode live `
  -InstallDir 'D:\VisitReady' -AppPort 9007
```

首次部署后端口保存在 `.env`。更新时直接重跑版本命令；需要改端口时先停止服务并编辑已有 `.env`。
