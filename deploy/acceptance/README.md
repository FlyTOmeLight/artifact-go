# OrbStack 隔离部署验收

在仓库根运行 `python3 deploy/acceptance/run.py`。需要已运行的 Docker/OrbStack、本仓库 Go 工具链、curl，以及本机已有以下镜像：

- `postgres:17-alpine`
- `redis:7.4.2-alpine`
- `busybox:1.37.0`
- `artifact-juicefs:1.4.1-arm64`（构建方法见 `../storage/build-juicefs.sh`）

脚本不拉取镜像，只创建 `artifact-drill-<随机值>` 独占网络、容器和卷，宿主端口只绑定 `127.0.0.1`，不读取 Kubernetes Secret，不连接已有数据库或 JuiceFS。临时 gateway 密钥保存在权限 `0600` 的临时文件中，结束移除文件、容器、数据卷和网络。Docker CLI 需要访问本机 socket；受限沙箱中需按平台授权运行。

引用验收直接调用进程内 `BindingService`，使用独立 PostgreSQL 验证绑定、释放、精确 scope 和持久引用归属。旧引用 HTTP 的 TLS、CA 拒绝与服务 token 轮换不再是当前架构的验收项；旧报告保留当时的实测记录。对外 tenant API 的 HTTPS 与管理凭据仍属于真实登录部署验收范围。

JuiceFS 部分以 8 个不同的 1 MiB 对象、并发 4 测量本机小样本写读；停止独立源 gateway 后，将 `juicefs dump` 的元数据与全部底层对象目录成对备份，恢复到新的 Redis 和对象卷，再用空缓存 gateway 读取所有对象并核对 SHA-256。保留原卷 UUID 和 `/data` 路径映射，原 gateway 停止后才启动恢复 gateway；不让两个元数据库同时写同一对象目录。

这不是容量上限测试，也没有在同一恢复点联合备份两业务数据库。业务双库恢复证据另见 `../../docs/t12-backup-restore-20261015.md`。生产数据库、对象存储、CA、入口、目标负载及 RPO/RTO 需要部署方提供参数后另行验收。


四层联合恢复另运行 `python3 deploy/acceptance/four_layer.py`。它在同一冻结点备份两业务数据库、JuiceFS 元数据与对象，恢复到全新资源后核对原版本和字节，再由真实 worker 继续删除义务。范围与实测结果见 [2026-10-16 记录](../../docs/four-layer-recovery-20261016.md)；不是默认 CI 或容量测试。

真实登录与重启回归使用 `forge-redeploy.cjs`，需要本机 tenant-web 的 Playwright、Chrome、既有隔离环境和 S3 端口转发。默认从 tenant-api 的私有 `.local/deployment.json` 读取登录配置，测试 scope 使用本目录 `forge-scope.json`。凭据只在内存使用，会话在结束时登出；报告只保存公开测试对象 ID。

```sh
# 首次不带 --verify，创建专用验收文件及版本；后续复用同一报告。
FORGE_REDEPLOY_REPORT="$PWD/.scratch/forge-orbstack/inprocess-acceptance.json" \
  node deploy/acceptance/forge-redeploy.cjs --verify --restart
```

`--restart` 明确重启 orbstack / forge-tenant-split 中的 artifact Deployment，等 rollout 就绪后立即真实登录、回读版本、请求下载授权、校验原文件字节，并验证删除状态及权限拒绝。请求失败直接退出，不重试。`FORGE_DIAGNOSTICS=1` 仅输出路径、状态、耗时和分类错误，不输出响应内容、会话或临时凭据。
