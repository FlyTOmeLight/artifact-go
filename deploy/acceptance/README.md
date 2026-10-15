# OrbStack 隔离部署验收

在仓库根运行 `python3 deploy/acceptance/run.py`。需要已运行的 Docker/OrbStack、本仓库 Go 工具链、curl，以及本机已有以下镜像：

- `postgres:17-alpine`
- `redis:7.4.2-alpine`
- `busybox:1.37.0`
- `artifact-juicefs:1.4.1-arm64`（构建方法见 `../storage/build-juicefs.sh`）

脚本不拉取镜像，只创建 `artifact-drill-<随机值>` 独占网络、容器和卷，宿主端口只绑定 `127.0.0.1`，不读取 Kubernetes Secret，不连接已有数据库或 JuiceFS。临时 gateway 密钥保存在权限 `0600` 的临时文件中，结束移除文件、容器、数据卷和网络。Docker CLI 需要访问本机 socket；受限沙箱中需按平台授权运行。

验收使用现有 `storagehandoff.Client`、真实 HTTP handler、`BindingService` 和独立 PostgreSQL，验证 HTTPS 证书校验、未知 CA 拒绝、服务凭据三阶段轮换、精确 scope 和引用归属。可信 CA 在该探针的 Go transport 中显式注入，不代表运行镜像的系统 CA 或生产入口已经验收。

JuiceFS 部分以 8 个不同的 1 MiB 对象、并发 4 测量本机小样本写读；停止独立源 gateway 后，将 `juicefs dump` 的元数据与全部底层对象目录成对备份，恢复到新的 Redis 和对象卷，再用空缓存 gateway 读取所有对象并核对 SHA-256。保留原卷 UUID 和 `/data` 路径映射，原 gateway 停止后才启动恢复 gateway；不让两个元数据库同时写同一对象目录。

这不是容量上限测试，也没有在同一恢复点联合备份两业务数据库。业务双库恢复证据另见 `../../docs/t12-backup-restore-20261015.md`。生产数据库、对象存储、CA、入口、目标负载及 RPO/RTO 需要部署方提供参数后另行验收。
