# 独立容器与外部 MySQL

生产环境推荐 Compose。一条 `docker run` 只适合已经有外部/托管 MySQL，或由其他平台负责容器生命周期的场景。

## 首次运行

`.env` 至少配置 live 模式、模型、外部 MySQL DSN 和会话加密密钥。容器中的 `localhost` 指向容器自身；数据库在宿主机时，Docker Desktop 使用 `host.docker.internal`，Linux 使用私网地址或显式 Docker 网络。

```bash
docker pull docker.io/asuka20011204/visit-ready-agent:1.0.4
docker run -d --name visit-ready-agent --restart unless-stopped \
  --init --read-only --tmpfs /tmp:size=16m,mode=1777 \
  --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 128 \
  --stop-timeout 130 --env-file /opt/visit-ready/.env \
  -e APP_ADDR=:8080 -e APP_VERSION=1.0.4 \
  -p 127.0.0.1:8097:8080 \
  docker.io/asuka20011204/visit-ready-agent:1.0.4
```

## 更新

先拉取并验证新镜像，再记录当前镜像 ID。停止旧容器后用同名参数创建新容器；`/readyz` 未通过时，用记录的旧镜像重新创建。不要依赖 `latest`，也不要在没有外部 MySQL 的情况下期待容器替换后保留内存会话。

Windows 项目目录中保留了 `scripts/update-local.ps1` 供既有独立容器迁移，但新的长期部署优先使用 `bootstrap-windows.ps1` + Compose。

## 外部 MySQL

数据库需要预先创建 `visitready` 库和非 root 用户，3306 只允许应用私网访问：

```sql
CREATE DATABASE visitready CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'visitready'@'%' IDENTIFIED BY '替换为随机密码';
GRANT ALL PRIVILEGES ON visitready.* TO 'visitready'@'%';
```

DSN 示例：

```dotenv
MYSQL_DSN=visitready:URL安全密码@tcp(mysql.internal:3306)/visitready?parseTime=true&charset=utf8mb4&loc=UTC
```
