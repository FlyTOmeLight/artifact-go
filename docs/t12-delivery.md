# T12 交付与验收矩阵

本页把系统 Spec 的 Testing Decisions 1–20 映射到现有测试与验收记录。测试名存在不等于本轮已执行；下表“已证实”引用对应 T01–T11 记录，“受控”表示外部 S3/身份故障由测试端点或 fixture 提供。T12 的最新统一运行结果见 [最终验收记录](t12-acceptance-20261015.md)，不能据此宣称真实身份、生产 TLS 或生产恢复已通过。

后续补充：[本机隔离部署验收](deployment-acceptance-20261015.md)已验证 TLS/服务凭据轮换与独立 JuiceFS 恢复；[租户能力核实](forge-tenant-capabilities-20261015.md)记录真实身份接入的可复用接口及缺项。

2026-10-16 后续：[Forge 接入交付](tenant-integration-acceptance-20261016.md)，成员准入与 active-only 规则已确认；此处历史矩阵的缺项按新记录逐项更新，不代表生产部署。

运行说明：[deploy/artifact/README.md](../deploy/artifact/README.md)。不新增公共 task API，不将配置 Header 当真实身份。

| Spec 测试项 | 对应测试与证据 | 状态及边界 |
| --- | --- | --- |
| 1 测试 seam | `TestArtifactHTTPAndSDK`、`TestReferenceHandoffTwoProcessesTwoDatabases`；T01/T09 | 已证实 HTTP/SDK+真实双库/进程；身份 fixture，外部故障受控 |
| 2 旧探针与当前合同区分 | T00、`deploy/storage/verification-20261015.md`、T06–T08 | 历史严格防覆盖/摘要探针非当前契约；须按当前实际验收记录判断 |
| 3 集合/作用域 | `TestArtifactHTTPAndSDK`、`TestServiceRequiresTrustedIdentity`、`TestResolveScopeTypeAndVisibility`；T01/T11 | 已证实规则；真实租户身份来源尚未接入 |
| 4 发布原子性/失败与重放 | `TestJSONVersions`、`TestTwoProcessesPublish`、`TestPublishUnknownCommitOutcome`、`TestReferencePublicationUnknownCommit`；T02/T09 | 已证实双库/跨进程；ACK 丢失受控 |
| 5 指纹/精度 | `TestJSONVersions`、`TestJSONNumericLexemesSurvivePersistence`、`TestFileDraftSuccessfulReceiptsSurviveLowerLimits`；T02/T10 | 已证实业务结果与数字词法，不用私有函数测试代替 |
| 6 alias | `TestAliasLifecycle`、`TestPublishAliasAtomicityAndReplay`；T03 | 已证实双库与条件更新、墓碑、原子发布 |
| 7 retention/删除 | `TestRetentionReplacementAndPreview`、`TestRetentionPublicationAtomicity`、`TestDeleteVersionLifecycle`、`TestFileRetentionPreservesDraftAndNewVersionReferences`；T04/T10 | 已证实规则及引用保护；物理故障受控 |
| 8 storage 文件/引用 | `TestStorageFileBatchStructureAndFields`、`TestStorageBindingAtomicityAndTombstones`、`TestStorageBindingReferenceIdentityRace`、`TestFileDraftReuseRenameRemoveAndLastRelease`；T07/T09/T10 | 已证实双库与独立交接；真实对象部分见 T07/T08 |
| 9 授权/Resolve/cursor | `TestStorageGrantRequestValidation`、`TestStorageGrantReadDeletionOrdering`、`TestResolveCollectionVersionAliasLatestAndFiles`、`TestPersistentURIRoundTrip`、`TestCursorRejectsDifferentScopeAndFilters`；T08/T11 | 已证实业务/URI规则；实际权限范围见 T08，身份仍 fixture |
| 10 管理 HTTP/SDK | `TestTenantBucketManagement`、`TestTenantBucketLive`、`TestStorageBuildRequiresVerifierBeforeResources`；T06 | 管理行为与真实后端已有记录；真实租户服务 verifier 接入尚缺 |
| 11 S3/STS adapter | `TestStorageGrantAdapter`、`TestStorageGrantLive`、`TestStorageGrantAdapterLive`、`TestStorageObjectExactKeyMultipartCleanup`；T07/T08 | 受控故障与真实 JuiceFS STS 已分别记录；跳过 live 测试不能算通过；生产网络/容量未验收 |
| 12 两域 worker/引用恢复 | `TestTwoProcessesClaimAndRecover`、`TestReferenceHandoffTwoProcessesTwoDatabases`、`TestReferencePlanPartialBatchCompensation`、`TestDraftReferenceRecovery`、`TestStorageBindingReleaseWaitsForWriteGrant`；T04/T07/T09/T10 | 已证实真实双库、多进程；崩溃/未知响应等由受控注入；无生产灾备证据 |
| 13 SDK/HTTP/OpenAPI | `TestDraftWireContract`、`TestStorageFileBatchWireContract`、`TestStorageGrantWireContract`、`TestResolveWireContract`、`TestResolveErrorsAndNoImplicitRetry`；T01–T11 统一检查 | 已证实 27 个公开操作及生成一致性；内部四操作不公开 |
| 14 生命周期/装配 | `TestProcessStartRequestSignalExit`、`TestStorageStandaloneDatabaseOwnership`、`TestInternalHandoffShutdownJoinsDatabaseHandler`、`TestServiceWorkerLifecycle`；T01/T07/T09 | 已证实测试入口装配/退出；stock server 缺 resolver 故意拒绝启动 |
| 15 完整系统判定 | T01–T11 分项记录、本页缺项 | 业务实现与本地验收不等于真实身份接入或生产上线；最终结论保留这些限制 |
| 16 草稿文件 CRUD/复制/发布 | `TestFileDraftFourPublicationFlows`、`TestFileDraftReuseRenameRemoveAndLastRelease`、`TestFileDraftPublicationRechecksSourceAfterBind`；T10 | 已证实单/多/文件夹清单及草稿发布，客户端自行编排上传 |
| 17 多页并发与另存 | `TestTwoProcessesEditDraft`、`TestFileDraftConcurrentEditorsCanSaveLosingAttachmentsAsNewDraft`、`TestDraftRequestKeyScope`；T05/T10 | 已证实两客户端/跨进程 API 行为；未交付 Web 编辑器，不能声称 UI 实测 |
| 18 创建幂等/来源删除 | `TestDraftUnknownCommitOutcome`、`TestDraftCopyListAndValidation`、`TestFileDraftCopyRechecksSourceVersionAfterBind`；T05/T10 | 已证实创建回执和来源竞争；每项覆盖以测试断言为准，不把编辑进程测试当所有创建跨进程场景 |
| 19 一致快照/历史删除 | `TestDraftConcurrentReadSnapshot`、`TestDraftLifecycle`、`TestFileDraftReuseRenameRemoveAndLastRelease`；T05/T10 | 已证实 JSON/附件快照和共享引用生命周期；真实 historical-scope resolver 尚缺 |
| 20 大小限制/显式大 JSON 文件 | `TestDraftJSONLimits`、`TestFileDraftLimitsAndExplicitJSONFile`、`TestFileDraftSuccessfulReceiptsSurviveLowerLimits`、`TestStorageFileBatchStructureAndFields`；T05/T07/T10 | 已证实字段/请求/文件各自边界，成功重放不重新按字段新限拒绝；不自动落文件 |

## T12 新增验收入口

- `TestPairedDatabaseBackupRestore` 对应测试决策 12、15：原生工具成对恢复业务数据库后继续处理持久义务，覆盖详情见 [恢复记录](t12-backup-restore-20261015.md)。
- `TestMaintenanceCLIRecovery` 对应测试决策 12、14、15：在两个数据库和两个域运行真实维护命令，验证预览、条件重排及审计。
- `TestFileDeliveryLive` 对应测试决策 8、9、11、12、16：实际 HTTP/SDK、内部 HTTP 和 JuiceFS 文件生命周期，显式启用 `ARTIFACT_TEST_DELIVERY_LIVE=1` 且运行超时至少 20 分钟。两个数据库用例同时等待真实写凭证期限，不改写期限，也不计入默认五分钟测试；实际结果以最终验收记录为准。

真实存储测试还需 `ARTIFACT_TEST_PG_DSN`、`ARTIFACT_TEST_MYSQL_PORT` 和 `ARTIFACT_TEST_S3_ENDPOINT`、`ARTIFACT_TEST_S3_ACCESS_KEY_ID`、`ARTIFACT_TEST_S3_SECRET_ACCESS_KEY`、`ARTIFACT_TEST_STS_ACCESS_KEY_ID`、`ARTIFACT_TEST_STS_SECRET_ACCESS_KEY`。从私有配置注入，STS 使用普通 IAM 身份；只连接隔离验收环境。运行命令：

```sh
go test -race -count=1 -timeout=20m ./tests/integration -run '^TestFileDeliveryLive$' -v
```

## 本轮镜像验证

2026-10-15 在当前 OrbStack 上构建 `artifact-go:t12-verification` 成功，使用官方 `golang:1.27.0-bookworm` manifest digest `sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452`，未升级 go.mod/go.sum。本地构建结果 manifest `sha256:7b2d7ef813309d8035b8dd38474a8b27430871abf13ff9c047c724d620cf1801`。未推送镜像。

- 镜像内 `artifact-maintenance -h` 退出 0，两个 domain 的参数可用；此命令不连接数据库。
- stock server 仅注入占位 DB URL，退出 1，明确报告 `artifact IdentityResolver is required; configure a trusted integration using BuildWithIdentity`，未尝试业务上线。
- 非 root scratch 镜像的构建和上述启动验证通过；多架构发布、实际配置挂载/证书链和生产鉴权仍未验收。

## 不能用测试 fixture 替代的剩余证据

真实租户系统如何验证用户、精确 scope 以及历史已删除归属尚待接入方确定；租户 bucket 管理验证器同样需要实际协议。生产证书、服务密钥分发、网络入口、备份恢复点和演练、容量及 RPO/RTO 由部署环境提供。真实浏览器文件选择/分片/续期链路如仍未执行，应保留为未验证，不能把 S3 SDK 或 HTTP 协议测试说成浏览器测试。
