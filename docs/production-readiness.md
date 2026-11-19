# OrbStack 生产化验收清单

日期：2026-11-18。版本基线：`v0.5.1`。本文跟踪 [Issue #10](https://github.com/FlyTOmeLight/artifact-go/issues/10)，验收对象固定为本机 OrbStack 的 `forge` 命名空间。当前没有独立生产环境，不再要求生产域名或外部 Ingress；清单用于把 OrbStack 当前实例推进到生产化验收状态。

记录每次验收时，写明环境、部署版本、镜像 digest、执行时间、命令入口、公开对象 ID、耗时和失败处理。不记录 session、token、STS 凭据、数据库口令、服务 token、完整数据库连接串或日志中的敏感值。公开仓库中的报告继续放在 `docs/verification.md` 或独立的生产验收记录中，脱敏后提交。

## 环境与输入

验收输入固定为 OrbStack 当前实例：

- Kubernetes context：`orbstack`；namespace 和 Helm release：`forge`。
- Artifact：Deployment `forge-artifact`，镜像 `artifact-go:v0.5.1`，api 与 TLS 代理同 Pod。
- 身份链路：Forge Tenant API `v0.13.0`，Service `forge-tenant`，透传白名单 `agentic-kit-api`。
- 数据服务：artifact 与 storage 两库在 PostgreSQL Service `forge-artifact-postgres`；对象存储是 JuiceFS Service `forge-juicefs`。
- 凭据体系：双库 DSN、S3/STS 密钥、service token 和租户管理 token 都引用 Secret `forge-artifact-env`；PostgreSQL 初始化口令在 `forge-artifact-db`。
- 公开入口：本机 Service `forge-web`，当前没有独立生产域名、外部 Ingress 或公网 TLS。
- 预计对象规模、配额、备份目的地、保留期、RPO 和 RTO 仍由使用者设定；历史 8 MiB 样本不能推导当前实例容量。

## 推荐顺序

按依赖顺序执行，前一项失败时先处理根因，不跳过记录：

1. OrbStack 配置与密钥。
2. OrbStack 网络与 TLS。
3. OrbStack 身份与普通业务链路。
4. 服务透传白名单。
5. 两域 doctor 巡检与人工恢复演练。
6. 联合备份恢复和 RPO/RTO 记录。

## 1. OrbStack 配置与密钥

- 使用 [Forge umbrella chart](../charts/forge/README.md) 或 [Artifact chart](../charts/artifact/README.md) 的最终 values 部署；不使用 `--reuse-values` 隐藏环境配置。配置校验入口是渲染后的 ConfigMap/Secret 引用和 Pod 启动参数。
- 凭据只引用既有 Secret；实际值不得写入仓库、values、ConfigMap、命令行参数、日志或验收报告。通过只读挂载或环境变量注入，运行配置由 `SERVER_CONFIG_PATH` 指向私有文件。
- `DB.AutoMigrate` 与 `StorageDB.AutoMigrate` 的取值与部署方案一致；Forge 模式沿用启动时 AutoMigrate。artifact 和 storage 保持独立数据库与事务。
- `StorageConsumerID` 非空且沿用该部署的既有值，升级时不重新生成；tenant API、bucket 管理、S3 根管理和 STS issuer 身份不得合并。
- 完成一次非数据库凭据的轮换演练：更新 Secret、滚动重启、旧凭据拒绝、新凭据成功；再完成一次撤销演练。记录重启窗口内的失败率。TLS 证书更换时同步验证客户端信任链，不关闭校验。
- 通过标准：新版本和既有数据在轮换后可读写，旧凭据明确失败，日志与配置不含敏感值。

## 2. OrbStack 网络与 TLS

- 从集群外访问公开业务入口，确认匿名请求返回 `401 AUTH_REQUIRED`。从部署配置指定的探针入口检查 `/healthz`，再从集群内验证 Service 到 artifact 进程、artifact 到 tenant API、artifact/storage 到对象存储的地址和证书。
- 使用实例内 CA 和证书链；自签 CA 必须加入镜像或挂载的可信集合，不能关闭校验。确认公开 Service、内部交接和 JuiceFS 出站地址。
- 核对公开路由只暴露 `/artifact/v1/`、`/storage/v1/` 及部署明确允许的入口；内部 `/internal/v1` 不对公开客户端开放。
- 滚动重启或 Helm 升级后，验证入口转发已指向新 Pod，再执行业务探针；必要时按部署环境确认 preStop 和 termination grace period。
- 通过标准：公开 HTTPS 成功、内部地址解析正确、匿名拒绝符合预期、跨证书和跨服务调用没有跳过 TLS 校验。

## 3. OrbStack 身份与业务链路

- 使用真实 Forge 登录会话和一个用户 API key，分别覆盖个人、本人共享、租户和项目四类 scope；跨范围访问必须拒绝。
- 用 Forge 租户系统的专用管理身份为验收租户开通 bucket，并确认普通用户凭据无法调用该管理入口。操作入口见[部署说明](../deploy/artifact/README.md#forge-session-入口)。
- 对每种 scope 执行文件 register、STS 上传授权、客户端直传、Complete、发布或草稿、固定版本 Resolve、STS 下载和 SHA-256 字节核对。字节由客户端直连对象存储，artifact 不代传文件内容。
- 用同一 requestKey 原参数重放一次创建，确认幂等结果；对显式删除后的版本验证墓碑语义和已释放引用。
- 通过标准：身份、scope、上传/下载字节、固定版本读取和跨范围拒绝全部在 OrbStack 公开入口通过；测试数据有明确清理或保留说明。

## 4. 服务透传白名单

- 先调用 Forge `/internal/v1/service/self` 获取真实服务名；接口返回 404 表示 tenant 部署未具备能力，不能据此判断 API key 无效。
- 仅把实测服务名加入 `artifact.forgeTenant.allowedPropagationServices`；普通用户会话和 API key 不加入白名单。
- 用下游服务自己的 Bearer 凭据发起请求，同时携带一致的 `x-forge-*` 与 `x-impersonate-target-*` 租户、用户和可选项目字段；`X-Resource-User-ID` 必须与目标用户一致。
- 验证白名单外服务拒绝、目标用户不存在或范围不一致拒绝、普通用户路径不受影响。
- 通过标准：授权服务可按目标用户完成一次固定版本读取和一次文件链路，非授权服务明确拒绝，配置与运行 ConfigMap 一致。

## 5. 两域 doctor 与人工恢复

- 使用私有只读数据库配置分别运行：

```sh
artifact-maintenance -action doctor -domain artifact \
  -db-config /run/secrets/artifact-db.json -limit 1000
artifact-maintenance -action doctor -domain storage \
  -db-config /run/secrets/storage-db.json -limit 1000
```

- 自动化检查 JSON 中的 `complete`、`errors` 和 `warnings`。遇到 `truncated=true` 可将 `-limit` 提高到最多 `10000` 后重跑；仍截断时，记录巡检覆盖缺口，保留该项待验收。
- 已知 warning 必须写明原因和处理计划；`expired_preparing` 可能是正常等待，failed 任务需要人工恢复。
- 选择一条测试产生的 failed 任务，按[失败恢复命令](../deploy/artifact/README.md#只读观察与失败恢复)填写数据库配置、任务 ID、预期 revision、操作者、原因和工单。先预览，核对摘要后用同一参数加 `-execute` 重排，并记录最终任务状态。
- 通过标准：两域 doctor 无 errors；warning 有解释；failed 任务通过 CAS 重排并由 worker 正常完成，不直接改表或删除墓碑。

2026-11-18 OrbStack 只读巡检结果：artifact 与 storage 均 `complete=true`、0 errors、0 warnings、无截断；storage 扫描 18 个 bound 文件和 13 个 deleted 文件。失败任务人工恢复演练仍待执行。命令使用临时端口转发和 0600 数据库配置，凭据与完整报告已删除。

## 6. 联合备份恢复与 RPO/RTO

- 备份范围必须同时覆盖 artifact 主库全部表、storage 主库全部表、JuiceFS 元数据、JuiceFS 对象数据及必要 IAM/配置密钥。单份数据库 dump、单份业务版本导出或单个 PVC 不构成恢复点。
- 使用维护窗口冻结新业务写入和授权，等待已签发写凭据上界与活动请求结束，停止两域 worker 和 JuiceFS 写入后记录同一恢复点；若采用在线 PITR，需另行设计并演练跨层一致性。
- 在隔离环境模拟一份备份副本缺失或损坏，验证能识别不可用的恢复点，并从另一套完整的匹配备份恢复。
- 在全新数据库、元数据和对象存储命名空间恢复，保持入口与 worker 关闭；核对 backend、bucket、`StorageConsumerID`、表行和对象，再启动 JuiceFS 与 artifact。
- 恢复后检查 `/healthz`、版本读取、固定版本字节 SHA-256、积压任务和 doctor；未完成的删除义务必须由真实 worker 推进，不插入伪造完成记录。
- 记录备份开始/结束、恢复开始/业务可用、数据校验时间和实际丢失窗口，形成本次 RPO/RTO 证据。历史四层演练的 8.321 秒只说明约 2 MiB 样例，不是当前实例的容量指标。
- 通过标准：恢复环境使用同一恢复点，保留版本可读且字节一致，待删义务继续完成，released 墓碑和共享引用语义保持不变。

## 已有机制作证边界

- [部署验收记录](deployment-acceptance-20261015.md) 证明隔离部署、TLS、凭据重装配和 8 MiB 字节恢复在本机通过；它不是当前 OrbStack 实例的容量和 RPO/RTO 证据。
- [四层联合恢复](four-layer-recovery-20261016.md) 证明两业务库与 JuiceFS 元数据/对象可在同一冻结点恢复；它使用测试身份和隔离 S3 操作者，不覆盖当前实例的真实 Forge 登录、STS、浏览器上传或容量。
- [T12 交付矩阵](t12-delivery.md) 汇总首期自动化和本机验收；OrbStack 验收完成后应新增记录，而不是改写这些历史证据。
- [运行说明](../deploy/artifact/README.md) 是网络、凭据、只读观察、人工恢复和备份边界的操作入口。
