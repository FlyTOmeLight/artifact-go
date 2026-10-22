# Artifact Helm chart

本 chart 管理 Artifact/Storage 组合服务、TLS/S3 代理、PostgreSQL、Service 和公开配置。凭据只引用已有 Secret；`postgres.existingClaim` 非空时引用已有 PVC，否则创建带 `helm.sh/resource-policy: keep` 的 PVC。卸载 release 不删除外部 Secret 或既有 PVC。

当前 OrbStack 部署由 [Forge umbrella chart](../forge/README.md) 统一管理，安装、升级与配置以该入口为准。原独立部署环境已退役，其本机 values 已移除。此 chart 不单独创建 tenant、web、Casdoor 或 JuiceFS；完整安装及 IAM 初始化由 umbrella chart 编排。

配置变更通过 Pod template 中的 checksum 自动触发更新。外部 Secret 内容变更后需重启对应的 Artifact Deployment；当前部署的资源名与操作入口见 [Forge 部署说明](../../deploy/forge/README.md)。不使用 `--reuse-values` 隐藏环境配置。

API 和代理容器退出前各等待 10 秒，在 Service 端点移除传播期间继续接收残余流量；Pod 总退出期限为 45 秒。使用 kubelet 执行的 `preStop.sleep`，scratch 镜像不需要添加 shell。OrbStack Kubernetes 1.36 已实测支持。此窗口已通过本机重启回归，其他环境应根据实际转发传播时间验收；详见 [重启故障记录](../../docs/restart-network-20261017.md)。

已退役环境曾将六个 Deployment/Service/ConfigMap 从 kubectl 迁移到 Helm：先备份、对比配置，再使用一次 `--take-ownership --force-conflicts` 接管。当时的 `artifact-integration-db` PVC 保持外部所有权；这不是当前安装步骤。

TLS Secret 包含 `ca.crt`、`artifact.crt`、`artifact.key`。数据库 Secret 提供 PostgreSQL 环境变量，init Secret 提供初始化 SQL，仅在空数据目录建库。应用 Secret 包含配置中 `${env:...}` 引用的变量。Forge 部署将 `DB.AutoMigrate` 与 `StorageDB.AutoMigrate` 均设为 `true`，服务启动时通过 `service.New` 和 `service.NewStorage` 执行 GORM AutoMigrate，保留当前 schema 的外键约束。Artifact/Storage 不使用独立迁移命令或 migrate initContainer；`storage-iam` initContainer 继续负责 JuiceFS IAM 初始化。全新整套安装使用 [Forge umbrella chart](../forge/README.md)，由它生成凭据、证书和双库初始化配置。

文件查询、绑定和释放在进程内调用，不配置内部 HTTP、服务 token 或监听端口。`StorageConsumerID` 必须与已有 storage_references 的归属一致；升级时保留该部署的现有值。
