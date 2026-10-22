# Artifact / storage 交付与运行

本镜像提供通用服务、可选 Forge 服务和维护命令三条二进制。Forge 入口已有 session 与 active 租户成员接入，需配置并部署配套的新 tenant scope 接口；本机部署已升级并完成真实登录和文件联调，重部署入口见 [Forge 本机部署](../forge/README.md)；生产身份接入另行验收。`cmd/server` 使用 `service.Build`；它因缺少 `IdentityResolver` 明确退出。不能通过读取普通请求 Header、自报 tenant/project 或把上游 403 解释为历史 deleted 状态来绕过这个边界。

## 构建及身份装配

在仓库根目录执行：

```sh
docker build -f deploy/artifact/Dockerfile -t artifact-go:local .
docker run --rm --entrypoint /usr/local/bin/artifact-maintenance artifact-go:local -h
```

构建固定 Go 1.27.1 官方镜像 digest，与 go.mod 一致。若构建网络无法访问默认 Go 模块代理，可用 `--build-arg GOPROXY=<可访问的公开模块代理>`；它只改变模块获取地址，不改变锁定版本或跳过校验。运行层为非 root 的 scratch，包含 CA、时区、三条静态二进制和无密钥的默认配置。专用 Dockerfile.dockerignore 只发送构建需要的源码及配置；不得将实际密钥写进 `conf/`。部署配置与凭据应在运行时只读挂载或通过部署密钥机制注入。自签 CA 需在构建或挂载时加入可信 CA 集合，不能关闭 TLS 校验。

接入方在自己的入口中把已经验证的身份函数交给以下公开装配函数，随后按相同 Dockerfile 构建入口；本仓库不假定租户系统的 HTTP 鉴权协议。

服务统一装配 artifact 与 storage 模块，使用 `BuildWithVerifiers(resolve, verify)`；不启用 storage 能力的组合应用仍可使用 `BuildWithIdentity(resolve)`。完整文件服务分别配置 artifact DB 与 StorageDB、S3/STS、用户身份及租户管理身份。文件引用直接调用 `internal/app/storage` 中的绑定业务，两个数据库保持独立事务。S3 客户端位于 `internal/client/storage`，storage 表模型位于 `internal/table/storage`。

`IdentityResolver` 必须验证本次调用身份及精确租户/项目归属，返回可信 UserID、TenantID、可选 ProjectID、归属 active/deleted。历史读取和成功请求确认需要可信历史归属；查不到或拒绝访问不能自行转换成 deleted。`TenantServiceVerifier` 仅验证租户系统的 bucket 管理身份，与普通用户身份分开。

配置字段的完整可运行语法以 [conf/server/config.toml](../../conf/server/config.toml) 为准。完整服务使用 DB/Version/Worker 和 StorageDB/Storage/StorageFiles/StorageWorker/StorageGrants，`StorageConsumerID` 指定持久引用归属。通用配置默认 `AutoMigrate=false`；Forge 部署将 `DB.AutoMigrate` 与 `StorageDB.AutoMigrate` 均设为 `true`，由服务启动时的 `service.New` 和 `service.NewStorage` 执行 GORM AutoMigrate，保留当前 schema 的外键约束。Artifact/Storage 不提供独立迁移命令，也不使用 migrate initContainer；`storage-iam` initContainer 继续负责 JuiceFS IAM 初始化。MySQL 使用 UTC 与 parseTime；连接池每域独立配置。

## Forge session 入口

`bin/artifact-forge-server` / 镜像内 `/usr/local/bin/artifact-forge-server` 使用 `[ForgeTenant]`，不影响原 `service.Build` 或自定义 resolver。构造时把 HTTP 客户端交给 Runtime；停止 HTTP 与两域 worker 后才关闭数据库和身份传输。

```toml
[ForgeTenant]
Endpoint = "${env:ARTIFACT_TENANT_ENDPOINT}"
ServiceToken = "${env:ARTIFACT_TENANT_SERVICE_TOKEN}"
Timeout = "15s"

# 启用 bucket 管理时配置：
# [TenantManagement]
# Tokens = ["${env:ARTIFACT_TENANT_MANAGEMENT_TOKEN}"]
```

其余资源沿现有配置。通过 `SERVER_CONFIG_PATH` 指向私有运行配置，镜像显式选择 Forge entrypoint；默认 entrypoint 仍是缺少注入即拒绝启动的通用服务器。两域保留独立数据库和事务。tenant API 与 bucket 管理凭据不可复用；进程内文件引用无需服务凭据。所有客户端配置固定在构造时，换 token 后需重启。

浏览器使用同源 `/artifact/v1/`、`/storage/v1/` 代理；Forge 前端 chart 的 `artifact.upstream` / `storage.upstream` 默认关闭，配置后只开放这两段公开 v1 路由，原 Cookie/Origin/CSRF 不改写。租户 PublicURL 必须匹配浏览器真实 Origin。程序调用则通过 SDK 的逐请求 Session/CSRFToken/Origin 传递现有 Forge 会话，不能拿服务 Bearer 代替用户。历史 scope 本轮拒绝。

需要先升级配套 tenant 源码（scope 接口及独立 bucket 任务），迁移新表并使用专用管理凭据启用其 worker；既有租户可运行 `server bucket-backfill` 幂等登记。任务失败后排除原因，`server bucket-retry TENANT_ID` 仅重排该 failed 任务。详见 sibling `forge-tenant-api/docs/storage-bucket-provisioning.md` 与[接口方案](../../docs/tenant-integration-design-20261016.md)。本机已完成配套部署；生产环境仍按上述步骤配置。

## 网络与凭据轮换

公开 HTTP 由接入层保护并终止 TLS，容器部署将公开监听地址配置为 `0.0.0.0:<port>`。文件引用不再有独立 HTTP 端口、内部 TLS 或服务 token。健康检查使用部署已有的进程/端口探测。

`StorageConsumerID` 是数据库中持久引用的归属标识，启用 StorageDB 时必填、无默认值，非空且至多 64 字节，在构造时固定；它不是认证凭据。升级现有部署必须保留该部署的既有值，避免已有引用无法查询或释放；当前 OrbStack 的配置入口见 [Forge umbrella chart](../../charts/forge/README.md)。用户精确 scope 检查仍在公开入口和文件绑定事务中执行。

对外调用 tenant API 的服务凭据、租户 bucket 管理凭据以及 S3/STS 密钥仍由部署环境管理。配置在构造时加载，轮换后需滚动重启；TLS 证书更换仍须确认对应客户端的信任链。

Storage 根管理凭据用于 bucket/回收，GrantIssuer 使用独立普通 IAM 身份；STS session policy 继续精确限制对象及 Actions。租户管理身份、S3 管理密钥及 STS issuer 不得合并。默认请求写超时 10 秒可能小于某些客户端长请求预算；120 秒是 attempt 固定上限，不保证每个同步 HTTP 请求可以运行 120 秒。客户端遇到结果未知按原 requestKey 核对。

## 只读观察与失败恢复

以下 SQL 分别在各域主库执行；不跨库 JOIN，不打印 task Payload 或任何配置密钥。时间均 UTC，先看数量和最早待处理时间，再按 ID 查失败原因。

```sql
-- artifact DB
SELECT kind, state, COUNT(*) AS count, MIN(next_run_at) AS earliest_due
FROM background_tasks GROUP BY kind, state;
SELECT id, kind, target_id, revision, attempts, total_attempts, run_generation,
       next_run_at, not_before, lease_until, last_error
FROM background_tasks WHERE state = 'failed' ORDER BY updated_at;
SELECT operation_kind, state, COUNT(*) AS count, MIN(deadline) AS oldest_deadline
FROM reference_changes GROUP BY operation_kind, state;
SELECT COUNT(*) AS expired_preparing FROM reference_changes
WHERE state = 'preparing' AND deadline < CURRENT_TIMESTAMP;
-- storage DB
SELECT kind, state, COUNT(*) AS count, MIN(next_run_at) AS earliest_due
FROM storage_tasks GROUP BY kind, state;
SELECT id, kind, target_id, revision, attempts, total_attempts, run_generation,
       next_run_at, not_before, lease_until, last_error
FROM storage_tasks WHERE state = 'failed' ORDER BY updated_at;
SELECT state, COUNT(*) AS count FROM storage_references GROUP BY state;
SELECT state, COUNT(*) AS count FROM storage_files GROUP BY state;
```

`not_before` 未到、等待物理删除回执、写授权未过期均可能是正常等待；released 引用墓碑会长期保留，数量不等于未完成积压。反复观察 due 时间、failed 和过期 preparing 是否持续增长，结合服务日志中的 task/attempt 身份调查。不要直接 DELETE 墓碑或把失败任务改为成功。

处理原因后，使用维护命令先预览，再以同一 expected-revision 执行；DB 配置为私有 JSON `db.Config` 文件，默认关闭迁移。容器使用只读 secret 挂载；下例文件路径、ID 和 revision 均为操作者填写值。

```sh
artifact-maintenance -domain artifact -db-config /run/secrets/artifact-db.json \
  -task-id TASK_ID -expected-revision REVISION -operator OPERATOR \
  -reason '原因已处理，重排本任务' -record TICKET
# 核对预览后，同一命令添加 -execute。
# storage 任务改用 -domain storage 及 storage DB 配置。
```

只有 failed 且无有效租约的任务可以重排。CAS 保留不可变载荷/ID/NotBefore，旧 lease token 失效，新一轮失败预算与审计记录持久保存。预览之后状态发生变化须重新读取，不绕开冲突。无公共任务 API。

## 备份与恢复边界

需备份四部分：artifact 主库全部表（包括成功回执、ReferenceChange、BackgroundTask）；storage 主库全部表（含 StorageReference 墓碑、StorageTask 和写授权上界）；JuiceFS 元数据；JuiceFS 实际对象数据及必要 IAM/配置密钥。单备份文件字节、单个 PVC 或只导出业务可见版本都不足以恢复引用协议。当前 OrbStack 的 JuiceFS 元数据与对象目录在同一个持久卷上，布局见 [charts/juicefs/templates/deployment.yaml](../../charts/juicefs/templates/deployment.yaml)；PVC 请求大小（10 GiB）不等于独立 SSD 配额或已验证容灾能力。

首期建议维护窗口协调快照：停止新业务写入及新授权，等待已发写凭据上界和活动请求结束，停止服务内两域 worker 与 JuiceFS 写入后，对上述各层记录同一恢复点。数据库和 JuiceFS 各用其存储设施支持的备份方式；文档不把文件系统在线复制当一致备份。若需要在线 PITR，必须另外设计并演练跨层恢复点。

恢复时先保持入口和 worker 关闭，恢复匹配的一组数据、配置与密钥，核对 backend/bucket、ConsumerID、表行和对象；再启动 JuiceFS 和 artifact 服务，检查积压和失败任务。不能混用新 artifact 快照与旧 storage 引用墓碑快照，否则可能删除仍被引用的对象；也不能重新生成 ReferenceID 或把 preparing 当成功发布。准备期限/写凭据上界按原时间保留。本地两业务域数据库的成对恢复已通过，见 [T12 恢复记录](../../docs/t12-backup-restore-20261015.md)；本机独立 JuiceFS 元数据与对象的冻结备份恢复已通过，见[隔离部署验收](../../docs/deployment-acceptance-20261015.md)。本机两业务库与 JuiceFS 四层同一恢复点的联合恢复也已通过，见[四层恢复记录](../../docs/four-layer-recovery-20261016.md)；生产恢复设施、RPO/RTO 及容量仍待最终环境验收。

## 尚未具备的生产接入证据

- 真实租户系统 IdentityResolver/TenantServiceVerifier 协议与历史归属确认，以及真实调用验证。
- 生产 TLS/CA、密钥分发与轮换、网络隔离及终止入口配置。
- 生产备份恢复演练、容量/并发指标与正式发布部署；远端 CI 结果见 [GitHub Actions](https://github.com/FlyTOmeLight/artifact-go/actions/workflows/check.yml)。
- 当前镜像默认 server 的 fail-closed 行为不等于上述集成已完成。

测试和外部验收证据逐项见 [T12 验收矩阵](../../docs/t12-delivery.md)。

## 自动指标与关联日志

运行入口使用 `app.Options{Setup: service.SetupHTTP}`；直接装配 HTTPHost 时将 `service.SetupHTTP` 作为 setup 参数。`[HTTPServer] MetricsEnable = true` 才注册框架 Prometheus `/metrics`，默认关闭。该入口是实例级运维数据，不适用租户身份；启用时由运维网络或入口代理限制访问，不增加业务 SDK 方法。此 setup 仅安装指标，不启用其他管理、调试或身份中间件。

| 信号 | 含义 |
| --- | --- |
| `artifact_service_requests_total{domain,method,route,status}` | 业务请求结果；201 发布成功、200 幂等确认、409 冲突、401/403 身份或状态拒绝分别可观测。route 是固定模板，404 不区分不存在和越权 |
| `artifact_storage_file_results_total{operation,result}` | 登记/确认逐项 success/failure，批量 HTTP200 内的失败也统计，不将 HTTP200 误当全部成功 |
| `artifact_storage_tombstone_probes_total{result}` | 已知 deleted 位置的每次探测：clean/object/multipart/error，后端不匹配及探测失败记 error，不冒充发现对象；不枚举未知 bucket key |
| `artifact_lock_acquire_seconds{operation}` | app 的 Artifact 行锁查询耗时，包含 SQL 往返及锁等待，不等同于数据库纯锁阻塞时间 |
| `artifact_service_task_failures_total{domain,kind}` | 已持久记录的真实执行失败；正常等待、失租、未确认提交不作为已确认失败 |
| `artifact_background_tasks{domain,worker_instance,kind,state}` | 每个运行 worker 所连接数据库的 pending/running/failed 快照 |
| `artifact_background_task_oldest_due_age_seconds{domain,worker_instance,kind}` | 到期 pending 或过期 running 租约的最老等待秒数，未到 notBefore 不算积压 |
| `artifact_background_task_collection_success` | 本次指标数据库读取是否成功；失败时不把任务数量伪报为零 |
| `artifact_retention_blocked_artifacts{reason="maxVersions"}` | 已启用策略上次评估后仍无法满足数量上限的集合数量 |

任务采集每次有 2 秒读取预算，仅查询所属域；worker 退出会注销采集器并等待已经开始的数据库读取。`worker_instance` 区分同进程多服务，Prometheus 目标标签区分副本；同一数据库的多个副本是重复快照，不能直接求和。按部署配置的数据库/服务目标分组取 max；请求与失败 counter 可以按副本求和。

`business_request` 日志关联 requestID、可信 tenantID/projectID、可用的 artifactID/versionID/draftID/fileID；后台失败日志关联 taskID、kind 和 targetID。ID 不进入指标标签，不记录 Header、请求正文或 STS。客户端到 S3 的实际字节上传没有经过服务，上传网络/分片失败须由客户端或 S3 监控提供；本服务只观测登记、授权、确认及回收结果。

初始告警建议：failed 数量大于零持续 1 分钟时检查 LastError 并按维护流程处理；采集失败持续 1 分钟时先检查数据库连接；到期任务最老等待超过 5 分钟时检查 worker、租约及外部服务。这些是开发默认租约60秒/扫描5秒下的初始阈值，生产按实际回收耗时调整。保留阻塞应通知集合维护方处理保护、alias 或数量策略，不能自动解除保护。

本机历史隔离验收曾验证引用 HTTP 的 TLS、token 轮换与 scope 拒绝；这些检查针对已移除的传输层。当前引用测试验证进程内调用、精确 scope 与持久引用归属；JuiceFS 两层及四层恢复仍需保留。复现入口见 [隔离验收](../acceptance/README.md)，历史结果见[原验收记录](../../docs/deployment-acceptance-20261015.md)。
