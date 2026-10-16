# Artifact Helm chart

本 chart 管理 Artifact/Storage 组合服务、TLS/S3 代理、PostgreSQL、Service 和公开配置。凭据只引用已有 Secret；`postgres.existingClaim` 非空时引用已有 PVC，否则创建带 `helm.sh/resource-policy: keep` 的 PVC。卸载 release 不删除外部 Secret 或既有 PVC。

`values-orbstack.yaml` 是当前本机环境的完整公开配置（JSON 格式，合法 YAML），包含镜像、已有资源名、tenant 接口地址及持久引用归属 StorageConsumerID。应用配置通过环境变量引用 Secret。此 chart 不创建 tenant、web、Casdoor 或 JuiceFS，也不初始化 IAM；生产环境需独立提供配置及数据库生命周期方案。

从 artifact-go 仓库执行：

```sh
helm lint charts/artifact -f charts/artifact/values-orbstack.yaml
helm upgrade --install artifact charts/artifact \
  --kube-context orbstack -n forge-tenant-split --reset-values \
  -f charts/artifact/values-orbstack.yaml --wait --timeout 180s
```

配置变更通过 Pod template 中的 checksum 自动触发更新。外部 Secret 内容变更后需另行执行 `kubectl rollout restart deployment/artifact`。不使用 `--reuse-values` 隐藏环境配置。

API 和代理容器退出前各等待 10 秒，在 Service 端点移除传播期间继续接收残余流量；Pod 总退出期限为 45 秒。使用 kubelet 执行的 `preStop.sleep`，scratch 镜像不需要添加 shell。当前 OrbStack Kubernetes 1.36 已实测支持。此窗口已通过本机重启回归，其他环境应根据实际转发传播时间验收；详见 [重启故障记录](../../docs/restart-network-20261017.md)。

现有六个 Deployment/Service/ConfigMap 从 kubectl 迁移到 Helm 时，已先备份、对比配置，再使用一次 `--take-ownership --force-conflicts` 接管。日常命令无需这两个参数。现有 `artifact-integration-db` PVC 保持外部所有权。

TLS Secret 包含 `ca.crt`、`artifact.crt`、`artifact.key`。数据库 Secret 提供 PostgreSQL 环境变量，init Secret 提供初始化 SQL，仅空数据目录执行。应用 Secret 包含配置中 `${env:...}` 引用的变量。现有数据升级不依赖自动建表，两个业务库均保持 `AutoMigrate=false`。

文件查询、绑定和释放在进程内调用，不配置内部 HTTP、服务 token 或监听端口。`StorageConsumerID` 必须与已有 storage_references 的归属一致；本机保留 `artifact-orbstack`，无需数据迁移。
