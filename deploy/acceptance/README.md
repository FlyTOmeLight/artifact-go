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

真实登录与重启回归使用 `forge-redeploy.cjs`，需要本机 tenant-web 的 Playwright、Chrome、既有隔离环境和 S3 端口转发。必须显式通过 `FORGE_CONFIG` 指定登录配置，或用 `FORGE_CONFIG_STDIN=1` 从 stdin 读取 JSON（含 `publicURL`、`adminUsername`、`adminPassword`）；不会自动读取旧 `.local` 缓存。scope 可放在登录配置的 `scope: {TenantID, ProjectID}`，或用 `FORGE_TEST_SCOPE` 指定公开 scope JSON；租户 ID 必须是 `t_`，项目 ID 必须是 `p_`。重启默认目标为 context `orbstack`、namespace `forge`、Deployment `forge-artifact`，分别通过 `FORGE_KUBE_CONTEXT`、`FORGE_NAMESPACE`、`FORGE_ARTIFACT_DEPLOYMENT` 覆盖。脚本通过真实 Casdoor 登录和 `/api/v1/me` 的 `user.id` 选择个人资源。会话与 STS 凭据仅在内存中使用，结束时登出；报告只保存本轮公开对象 ID。

```sh
# 首次运行，创建四种scope的专用数据、删除样例和个人引用释放任务。
FORGE_CONFIG=/path/to/restricted-login.json FORGE_TEST_SCOPE=/path/to/scope.json \
FORGE_REDEPLOY_REPORT="$PWD/.scratch/forge-orbstack/inprocess-acceptance.json" \
  node deploy/acceptance/forge-redeploy.cjs

# 部署重启后复用同一报告核对四种scope、worker删除样例和引用释放。
FORGE_CONFIG=/path/to/restricted-login.json FORGE_TEST_SCOPE=/path/to/scope.json \
FORGE_REDEPLOY_REPORT="$PWD/.scratch/forge-orbstack/inprocess-acceptance.json" \
  node deploy/acceptance/forge-redeploy.cjs --verify --restart
```

每种 scope 都执行文件 register、STS PUT、Complete、版本或草稿发布、Resolve 和 STS GET 字节核对；跨范围拒绝逐项检查。完整 requestKey 并发矩阵复用 `TestCollectionCreateIdempotency`。

引用释放按服务端回执推进：删除共享引用版本后重启 artifact，轮询同一版本的 DELETE 回执，直到返回 `deleted`，再核对末引用版本仍可读且能取回原始字节。随后删除末引用版本，轮询存储文件直到 `deleted`。每次轮询有独立的 20 分钟期限。

上传授权回收使用未绑定的删除样例，按服务实际签发的 `expiresAt` 检查。当前 STS 要求至少 900 秒加签发余量，因此采用 chart 配置的 16 分钟授权。删除后立即开始轮询：授权未到期时必须保持 `deleting`，到期后等待 `deleted`。报告分别保存到期前保护状态和删除状态的观察时间；轮询不能给出精确物理删除时刻。如果中断导致没有观察到到期前状态，续跑不会将该项判为通过。

脚本在创建任何资源前先写入报告身份，之后每完成一个阶段单独保存（`0600`），不会覆盖已有报告。创建阶段中断的报告标记为 `creating`，不能用 `--verify` 续建，需先核查该次创建的资源；报告并非每次 API 写入的完整日志。资源创建完成后，`--verify` 可继续删除及引用释放检查，不沿用上次运行的截止时间；已完成的引用释放仍查询文件是否保持 `deleted`。旧报告缺少四类 scope、上传授权或引用释放阶段证据时拒绝复验，不能用旧版 `completed` 标记证明新版检查通过。`--restart` 在登录和创建资源之后、开始核对之前重启一次 Artifact；重启流程等待旧 Pod 删除，STS 传输前有界等待 S3 入口返回匿名拒绝，避免转发仍连接旧 Pod 时立即读文件。引用释放流程本身在首次删除版本后还会再重启一次，用来检查待处理任务跨重启仍会完成。失败日志仅含阶段、状态和公开 file ID，不打印 session、token 或 STS 凭据。
