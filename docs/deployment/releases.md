# 版本发布与其他机器更新

这里区分两个动作：开发者发布新镜像；每台目标机器选择何时升级。发布成功不会自动接触服务器密钥或数据库。

## GitHub Actions 自动发布

`.github/workflows/release.yml` 在推送 `v*` tag 后运行 Go、race、覆盖率、vet、浏览器 E2E 和镜像构建，然后发布 GHCR。启用 Docker Hub 时，在 GitHub 仓库 Actions 中配置：

- Secret `DOCKERHUB_USERNAME`
- Secret `DOCKERHUB_TOKEN`（Docker Hub Access Token，不是账户密码）
- Repository variable `DOCKERHUB_PUBLISH_ENABLED=true`

开发完成后的标准流程：

```powershell
git status
go test ./...
go vet ./...
git add .
git commit -m "feat: prepare visit ready release"
git push origin main
git tag v1.0.3
git push origin v1.0.3
```

不要移动或覆盖已经发布的 tag。`v1.0.3` 已经发布，后续修复必须使用新标签（例如 `v1.0.4`）。等待 `release-image` 工作流成功后，确认仓库出现：

```text
docker.io/asuka20011204/visit-ready-agent:1.0.3
ghcr.io/asuka-20011204/visit-ready-agent:1.0.3
```

发布工作流会生成 SBOM/provenance，并在 Job Summary 输出镜像 digest。记录该 digest 和 40 位提交 SHA；生产部署把它们传给一键脚本。安装脚本从 `main` 下载，脚本会校验镜像 OCI revision 并从同一提交下载 Compose。

具体获取路径：打开 GitHub 仓库的 `Actions` -> `release-image` -> 对应成功版本 -> `Summary`。`publish` Job 对应 GHCR，`publish-dockerhub` Job 对应 Docker Hub。复制所选 Job 的 `Source commit` 与 `Published digest`；不要把一个仓库的 digest 用于另一个仓库。

当前 `v1.0.3` 的记录如下：

| 镜像仓库 | SourceCommit | ImageDigest |
| --- | --- | --- |
| Docker Hub（脚本默认） | `fcc7021129d15cdaa9df75175559a0cf233c2c67` | `sha256:caa353cd12a2912e58f45b6d5fe3b5f2d96f906dda0840261cc9b437ebc66e1e` |
| GHCR | `fcc7021129d15cdaa9df75175559a0cf233c2c67` | `sha256:bb5d0dcd644ecf1237dae869d22cc8697719e370ef47255b5a469421970ba916` |

上一成功版本 `v1.0.2`：

| 镜像仓库 | SourceCommit | ImageDigest |
| --- | --- | --- |
| Docker Hub | `e03a9f67e2a17c32ab63501af033e7bb2ba8dd18` | `sha256:223fb854a7e644a0d25ac57a2392643e7e6fdf1ca6043277845f56220261229a` |
| GHCR | `e03a9f67e2a17c32ab63501af033e7bb2ba8dd18` | `sha256:91be2428135acdf6a6fd056ab2215386e4f96a8095896aad106f7389429cd0f0` |

## 本机手动发布 Docker Hub

```powershell
docker login
git tag v1.0.3
git push origin v1.0.3
.\scripts\publish-release.ps1 `
  -Version 1.0.3 `
  -RegistryImage docker.io/asuka20011204/visit-ready-agent `
  -Push
```

脚本要求工作区干净、`v1.0.3` 正好指向当前完整提交且该 tag 已推送到 `origin`；任一 Git、Go 或 Docker 命令失败都会终止发布。Docker Hub 仓库应启用不可变标签策略，进一步防止覆盖已发布版本。

## 让其他机器更新

其他机器不会因为仓库出现新镜像而自动替换运行中的容器。发布成功后，先从 `main` 下载安装脚本，再在每台机器明确执行对应更新命令：

```powershell
$SourceCommit = 'fcc7021129d15cdaa9df75175559a0cf233c2c67'
$ImageDigest = 'sha256:caa353cd12a2912e58f45b6d5fe3b5f2d96f906dda0840261cc9b437ebc66e1e'
Invoke-WebRequest `
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/main/scripts/bootstrap-windows.ps1" `
  -OutFile bootstrap-windows.ps1
Set-ExecutionPolicy -Scope Process Bypass
.\bootstrap-windows.ps1 -Version 1.0.3 `
  -ExpectedSourceCommit $SourceCommit `
  -ExpectedImageDigest $ImageDigest
```

```bash
SOURCE_COMMIT='fcc7021129d15cdaa9df75175559a0cf233c2c67'
IMAGE_DIGEST='sha256:caa353cd12a2912e58f45b6d5fe3b5f2d96f906dda0840261cc9b437ebc66e1e'
curl --proto '=https' --proto-redir '=https' -fsSL \
  "https://raw.githubusercontent.com/Asuka-20011204/visit_ready_agent/main/scripts/bootstrap-server.sh" \
  -o bootstrap-server.sh
chmod +x bootstrap-server.sh
sudo env EXPECTED_SOURCE_COMMIT="$SOURCE_COMMIT" \
  EXPECTED_IMAGE_DIGEST="$IMAGE_DIGEST" \
  REGISTRY_IMAGE=docker.io/asuka20011204/visit-ready-agent \
  ./bootstrap-server.sh 1.0.3
```

建议先升级测试机并完成账号登录、历史恢复和一次 Agent 流程，再分批升级生产机。不要使用定时拉取 `latest` 的 Watchtower 作为医疗数据服务默认更新策略，因为它绕过质量确认和迁移检查。

## 回退版本

再次执行旧版本号，并同时提供该旧版本记录的提交 SHA 与镜像 digest，即可回退应用。一键脚本也会在每次成功部署后保存版本、digest 和源码提交，供同一次失败切换自动恢复。若新版本包含数据库迁移，先阅读发布说明确认向后兼容；必要时按 [运维手册](runbook.md) 从升级前备份恢复数据库。应用镜像回退本身不会自动撤销数据库数据变化。
