# Windows 一键部署

适用于 Windows 10/11 + Docker Desktop。无需 Go、Node.js 或 Git；首次运行前启动 Docker Desktop，并确认 `docker compose version` 可用。

## 获取 SourceCommit 和 ImageDigest

这两个值来自成功的镜像发布记录，不是自行生成的随机值：

- `SourceCommit`：该版本标签实际构建的 40 位 Git 提交 SHA，用于确保下载的部署脚本、Compose 文件与镜像来自同一份源码。
- `ImageDigest`：镜像仓库为该镜像内容生成的 `sha256:` 摘要。Docker Hub 和 GHCR 的摘要可能不同，必须使用与 `RegistryImage` 相同仓库的摘要。

在 GitHub 页面打开仓库，依次进入 `Actions` -> `release-image` -> 选择成功的版本运行（例如 `v1.0.2`）-> `Summary`。页面底部有两个发布 Job：

- `publish`：GHCR 的 `Source commit` 和 `Published digest`。
- `publish-dockerhub`：Docker Hub 的 `Source commit` 和 `Published digest`。

脚本默认使用 Docker Hub，所以应复制 `publish-dockerhub` 的 digest。当前 `v1.0.2` 可直接填写：

```powershell
$SourceCommit = 'e03a9f67e2a17c32ab63501af033e7bb2ba8dd18'
$ImageDigest = 'sha256:223fb854a7e644a0d25ac57a2392643e7e6fdf1ca6043277845f56220261229a'
```

也可以安装 GitHub CLI 后在仓库目录查看发布日志：

```powershell
gh run view 34348196449 --log |
  Select-String 'Source commit:|Published digest:'
```

输出前缀为 `publish-dockerhub` 的摘要属于 Docker Hub；前缀为 `publish` 的摘要属于 GHCR。还可以用 `git rev-list -n 1 v1.0.2` 单独获取 `SourceCommit`，但生产部署仍应以成功发布运行显示的值为准。

## 首次 Demo

从发布工作流摘要复制 40 位源码提交。即使是 Demo，脚本也固定从不可变提交下载，因为它拥有 Docker 和部署目录的写权限。`v1.0.2` 示例：

```powershell
$SourceCommit = 'e03a9f67e2a17c32ab63501af033e7bb2ba8dd18'
Invoke-WebRequest `
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/$SourceCommit/scripts/bootstrap-windows.ps1" `
  -OutFile bootstrap-windows.ps1
Set-ExecutionPolicy -Scope Process Bypass
.\bootstrap-windows.ps1 -Version 1.0.2 -Mode demo
```

打开 `http://127.0.0.1:8097`。Demo 不调用模型，不创建账户，适合先确认界面和容器环境。

## 首次 Live

```powershell
.\bootstrap-windows.ps1 -Version 1.0.2 -Mode live
```

脚本会依次安全询问模型端点、模型密钥、模型名和可选博查密钥，自动生成 MySQL 密码与会话加密密钥。配置和 Compose 文件默认保存在 `%LOCALAPPDATA%\VisitReady`。

打开 `http://127.0.0.1:8097`，创建邮箱账户，再按仓库 README 的完整演示流程操作。本机 HTTP 使用 `AUTH_COOKIE_SECURE=false`；不要把该设置复制到公网。

## 公网 Live

从 GitHub Actions 的 `publish-dockerhub` 发布摘要复制 40 位源码提交和 `sha256:` 镜像摘要。按固定提交下载脚本，并把两项校验同时传给部署命令。下面是可直接执行的 `v1.0.2` Docker Hub 示例：

```powershell
$SourceCommit = 'e03a9f67e2a17c32ab63501af033e7bb2ba8dd18'
$ImageDigest = 'sha256:223fb854a7e644a0d25ac57a2392643e7e6fdf1ca6043277845f56220261229a'
Invoke-WebRequest `
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/$SourceCommit/scripts/bootstrap-windows.ps1" `
  -OutFile bootstrap-windows.ps1
Set-ExecutionPolicy -Scope Process Bypass
.\bootstrap-windows.ps1 -Version 1.0.2 -Mode live `
  -AuthCookieSecure $true `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

若改用 GHCR，则必须同时指定 GHCR 地址和它自己的摘要：

```powershell
$ImageDigest = 'sha256:91be2428135acdf6a6fd056ab2215386e4f96a8095896aad106f7389429cd0f0'
.\bootstrap-windows.ps1 -Version 1.0.2 -Mode live `
  -RegistryImage 'ghcr.io/asuka-20011204/visit-ready-agent' `
  -AuthCookieSecure $true `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

应用仍只绑定 `127.0.0.1:8097`。由 Caddy、Nginx 或云负载均衡器提供 HTTPS，再反向代理到该地址；不要直接开放应用或 MySQL 端口。

## 更新与回退

发布 `1.1.0` 后执行：

```powershell
.\bootstrap-windows.ps1 -Version 1.1.0
```

脚本保留原 `.env` 与 MySQL 卷，拉取并切换新镜像，只有 `/readyz` 成功后才写入新版本号。失败会尝试恢复上一应用版本。手动回退同样重跑旧版本：

```powershell
.\bootstrap-windows.ps1 -Version 1.0.2
```

应用回退不等于数据库回退。涉及不兼容迁移时，先按 [运维手册](runbook.md) 备份并验证恢复。

公网生产更新必须传入发布摘要中的 `-ExpectedSourceCommit` 与 `-ExpectedImageDigest`，把 Compose 源提交和镜像内容固定到同一发布记录。脚本会保存成功版本的 digest 和源码提交；自动回退时若旧本地标签不存在，会使用已保存的 digest 拉取并核对旧镜像。

## 自定义目录或端口

```powershell
.\bootstrap-windows.ps1 -Version 1.0.2 -Mode live `
  -InstallDir 'D:\VisitReady' -AppPort 9007
```

首次部署后端口保存在 `.env`。更新时直接重跑版本命令；需要改端口时先停止服务并编辑已有 `.env`。
