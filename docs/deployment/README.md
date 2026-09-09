# 部署决策索引

本目录把“构建和发布镜像”与“目标机器部署镜像”分开。生产更新只使用不可变版本号或 digest，不使用 `latest`。

## 先选择运行目标

| 条件 | 推荐路径 | 是否需要源码 | 数据方式 |
| --- | --- | --- | --- |
| Windows 10/11，有 Docker Desktop | [Windows 一键部署](windows.md) | 否 | demo 内存；live MySQL 卷 |
| Ubuntu 空白服务器 | [Linux 一键部署](linux.md) + `INSTALL_DOCKER=true` | 否 | live MySQL 卷 |
| 其他 Linux，已有 Docker + Compose v2 | [Linux 一键部署](linux.md) | 否 | live MySQL 卷 |
| 已有托管 MySQL 或必须使用 `docker run` | [独立容器](standalone.md) | 否 | 外部 MySQL |
| Kubernetes 集群 | [Kubernetes](kubernetes.md) | 仅需清单 | 外部 MySQL |
| 开发者发布新镜像 | [版本发布与机器更新](releases.md) | 是 | 不接触生产数据 |

## 模式选择

- `demo`：无模型密钥、无账户、内存会话，适合离线看界面。
- `live`：真实模型、强制账户、加密 MySQL，适合完整功能和公网部署。
- 公网部署只使用 `live`，必须通过 Caddy/Nginx/云负载均衡器提供 HTTPS，并保持 `AUTH_COOKIE_SECURE=true`。

## 端口约定

- 浏览器访问的默认宿主机端口：`8097`。
- 镜像内部监听端口：`8080`。
- Compose 默认只绑定 `127.0.0.1:8097`，不会直接暴露到公网。
- 反向代理应转发到 `http://127.0.0.1:8097`。

## 生命周期

```text
开发与测试 -> 创建不可变版本标签 -> CI 发布镜像
       -> 目标机器运行一键脚本 -> /readyz 通过 -> 提交当前版本
       -> 新版本失败 -> 应用版本回退 -> 检查数据库备份
```

环境变量以 [configuration.md](configuration.md) 为唯一说明来源，日常操作与故障恢复见 [runbook.md](runbook.md)。
