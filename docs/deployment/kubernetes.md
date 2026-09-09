# Kubernetes 部署

Kubernetes 清单位于 `deploy/k8s/`，适合展示或已有集群的部署，不是单机首选路径。live 模式需要外部 MySQL。

1. 把 `deploy/k8s/deployment.yaml` 中的示例镜像替换为实际镜像 digest。
2. 创建命名空间和 Secret；密钥不要写入 ConfigMap 或提交到仓库。
3. 应用 Kustomize 清单并观察 rollout。

```powershell
kubectl apply -f deploy/k8s/namespace.yaml
kubectl -n visit-ready create secret generic visit-ready-secrets `
  --from-literal=LLM_API_KEY='替换' `
  --from-literal=BOCHA_API_KEY='替换' `
  --from-literal=MYSQL_DSN='visitready:密码@tcp(mysql.internal:3306)/visitready?parseTime=true&charset=utf8mb4&loc=UTC' `
  --from-literal=SESSION_ENCRYPTION_KEY='替换为32字节Base64URL密钥'
kubectl apply -k deploy/k8s
kubectl -n visit-ready rollout status deployment/visit-ready-agent --timeout=120s
kubectl -n visit-ready port-forward service/visit-ready-agent 8097:80
```

打开 `http://127.0.0.1:8097`。公网入口必须使用 HTTPS Ingress，并让认证 Cookie 保持 Secure。

当前清单固定为单副本和 `Recreate`。限流与并发槽仍在单实例内存中；提供 Redis 等共享后端前不要盲目扩容。更新时先备份数据库，再更新镜像 digest；失败使用 `kubectl rollout undo` 只能恢复应用版本，不能恢复数据库迁移。
