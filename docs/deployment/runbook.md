# 部署运维手册

## 健康和状态

```bash
cd /opt/visit-ready
docker compose -f compose.yaml -f compose.mysql.yaml ps
curl -fsS http://127.0.0.1:8097/livez
curl -fsS http://127.0.0.1:8097/readyz
docker compose -f compose.yaml -f compose.mysql.yaml logs --tail 100 app
```

- `/livez` 仅表示进程存活。
- `/readyz` 在 2 秒预算内检查会话存储和容量；模型供应商短时故障不会让容器反复重启。
- `/healthz` 是 `/readyz` 的兼容入口。

## 重启与停机

```bash
docker compose -f compose.yaml -f compose.mysql.yaml restart app
docker compose -f compose.yaml -f compose.mysql.yaml down
```

普通 `down` 保留 MySQL 命名卷。`down -v` 会删除账户和健康会话，只能在确认永久清除数据且已有必要备份时执行。

## 备份

Linux 和 Windows 一键脚本都会在 live 更新前创建逻辑备份与 SHA-256 校验文件。Linux 默认写入 `/opt/visit-ready/backups` 并设为仅部署用户可读；Windows 默认写入 `%LOCALAPPDATA%\VisitReady\backups` 并移除继承权限，只保留当前用户、SYSTEM 和本机管理员。另应定期执行由托管数据库或服务器平台提供的加密备份并测试恢复。单独备份数据库不够：还必须在安全的异机位置备份 `.env` 中的 `SESSION_ENCRYPTION_KEY`。

不得把 `.env` 上传到仓库、工单或普通聊天。备份文件包含敏感健康数据和账户摘要，应限制访问并设置留存期限。

## 更新失败

1. 查看 `app` 与 `db` 日志，但不要把包含密钥的完整环境输出发送到公共渠道。
2. 检查 `/opt/visit-ready/.env` 中 `APP_VERSION` 是否仍是最后成功版本。
3. 使用发布记录中的旧版本号、提交 SHA 和镜像 digest 重跑最后成功版本的一键脚本。
4. 若数据库迁移不兼容，停止应用，先在隔离环境验证升级前 SQL 备份，再执行数据恢复。

## 配置丢失

若 `.env` 丢失，不要生成新的 `SESSION_ENCRYPTION_KEY` 覆盖旧值；新密钥无法解密历史会话。一键脚本检测到部署标记或 Compose 卷但缺少 `.env` 时会拒绝继续。

## 磁盘维护

定期检查 Docker 日志、旧镜像与 SQL 备份的占用。一键脚本在部署目录保存 `.image-digest`、`.source-commit`，并在 `.rollback` 中保留上一成功版本的对应元数据；归档或删除旧镜像前先保存发布记录。不要用宽泛的 `docker system prune --volumes`，它可能删除数据库卷。
