# T12：交付验收与外部接入边界

状态：本地全量验收、运维与镜像检查、真实 JuiceFS 长链路均已通过。真实租户身份接入尚未验收，因此不将 T12 标记为完整通过，也不宣称生产可上线。

## 本轮完成

- 新增 `TestPairedDatabaseBackupRestore`：在 PostgreSQL 17 / MySQL 8.4 上分别冻结两域写入，使用真实 dump/restore 工具还原到新的一对数据库。验证发布回执、文件清单、有效引用、过期 preparing、完整补偿计划、未发送引用墓碑及删除义务。详见 [恢复记录](t12-backup-restore-20261015.md)。
- 新增 `TestMaintenanceCLIRecovery`：实际构建并运行维护 CLI，覆盖两种数据库和两个 domain。dry-run 整行不变；execute 保留不可变载荷、目标和 notBefore，留下审计；旧 revision 重放被拒。
- 新增 `TestFileDeliveryLive`：真实公开 HTTP/Go SDK、两个独立业务数据库、内部 HTTP Bind/Release 与实际 JuiceFS；覆盖文件夹草稿发布、Resolve、下载、共享引用保护、最后释放和写凭证自然到期后的物理回收。真实身份由明确标记的受控 fixture 提供。
- 增加非 root 多阶段镜像及 [部署说明](../deploy/artifact/README.md)，包含三种装配、TLS/服务密钥轮换、只读积压查询、人工恢复和四层备份边界。默认 server 缺少可信 resolver 时明确退出。
- 建立 [Spec 测试决策 1–20 对照矩阵](t12-delivery.md)。

## 实际检查

| 检查 | 结果 |
| --- | --- |
| `make check` | 退出 0；全量双库 race、vet、两个命令构建、格式、OpenAPI 重生成一致性通过 |
| 全量 Go 测试 | integration 111.190 秒；SDK 1.831 秒；unit 3.495 秒 |
| 成对备份恢复定向 race | 4.642 秒通过；全量检查再次覆盖 |
| 真实维护 CLI 定向 race | 15.677 秒通过；全量检查再次覆盖 |
| 实际 JuiceFS bucket / file / grant / adapter 回归 | 4 组通过，2.576 秒；该次 adapter 未启用额外到期等待 |
| 浏览器 CORS | 独立无头 Chrome 的签名 PUT、GET、HEAD、Range 全部通过；未连接用户现有浏览器会话 |
| 部署镜像 | 构建成功，maintenance help 退出 0；stock server 因缺 resolver 退出 1，符合 fail-closed 契约；未推送 |
| 完整文件链路与实际到期回收 | 修正后双库 race 通过，911.786 秒；明确鉴权拒绝、管理 HEAD 正例、迟到对象 GC 和 artifact 物理回执全部通过 |

浏览器首轮因 Playwright 默认浏览器未安装而未执行；改用已安装 Chrome 的独立临时实例后通过。不是安装浏览器，也未接管人工会话。

## Review 与修正

Standards / Spec 分别审查后修正两处测试问题：storage CLI fixture 使用真实 `delete` 任务；到期 PUT 必须返回明确鉴权拒绝，并用管理身份 HEAD 同一对象成功排除网络故障。最后释放后的新读取授权也核对精确 `409 FileNotReadable`。增量复核均为 0 项遗留；严格断言的首次运行发现 JuiceFS 实际返回 `403 InvalidAccessKeyId`，已与既有后端测试统一允许明确的 `InvalidAccessKeyId` / `InvalidToken` / `ExpiredToken` / `AccessDenied`，仍不接受任意网络错误。最终修改后重新跑完整真实有效期并通过，未改写任何数据库期限；相关 vet 与增量 Standards 复核也通过。

本轮共使用 4 个子代理，分别承担业务实现/测试以及独立审查，T10–T12 期间复用；T12 的备份恢复、CLI、交付文档与两种审查分工均已完成。

## 尚未验收

1. 真实租户系统的 IdentityResolver / TenantServiceVerifier、历史归属及实际调用。缺外部协议与环境，未编造 Header 鉴权或跨仓库修改。
2. 生产 TLS/CA、密钥分发轮换、网络入口、生产容量与 RPO/RTO。
3. JuiceFS 元数据与底层对象字节的完整灾备恢复。本轮成对恢复仅证明两业务数据库的决策和引用一致性。
4. 浏览器文件选择、分片与续期的完整 UI 编排。当前真实浏览器仅验证 CORS 读写；multipart/续期由 S3/SDK 验证。

不自动提交、推送、发布或部署到生产。上述外部接入与部署证据不能用本地 fixture、测试通过或镜像构建替代。

## 清理

本轮临时 IAM 用户与策略已删除，私有临时凭据文件与管理辅助程序已清理；测试创建的 bucket/object 由各测试清理，两只一次性数据库容器已删除，本轮端口转发已停止。保留既有 JuiceFS 部署、PVC 和根凭据；保留本地验收镜像供 review，未推送。
