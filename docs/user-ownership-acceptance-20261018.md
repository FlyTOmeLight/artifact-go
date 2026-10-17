# Issue #2 可选用户归属与集合幂等创建验收

日期：2026-10-18。范围对应 GitHub Issue #2。本地实现与验证，不包含提交、推送、发布或现有部署数据库迁移。

## 交付

- Artifact 与 StorageFile 新增可空 UserID；CreatedBy 保持实际操作者。共享 scope_key 保留原编码，个人 scope_key 使用无歧义的项目长度前缀及用户归属，相关列扩大至 160 字符。
- SDK 使用 RequestIdentity.ResourceUserID 传递 X-Resource-User-ID；省略选择共享，显式空拒绝。IdentityResolver 返回独立 ResourceUserID，中间件与写前复验检查选择一致；旧 resolver 忽略新选择时拒绝，而不是访问共享范围。
- 内置 Forge 复用当前租户/项目验证，只接受共享或当前用户自己的个人归属，不修改 tenant 服务的接口。
- 子资源和游标继承完整范围，文件 Metadata/Bind/Release/Statuses 与持久引用计划携带固定用户归属，后台释放从原计划或父集合取得范围。
- 创建集合新增可选 requestKey，集合行保存成功事实与可空创建键。首次201、重放200，规范参数异参409，归档及历史范围重放返回当前状态与 retention；无键不按 NULL 键查询恢复。
- 沿用 AutoMigrate，旧行 user_id 为 NULL，不从 created_by 回填；新索引保护完整范围、实际创建者和请求键。
- README、全部范围接口注解、OpenAPI 及独立设计同步。

## 关键测试

`tests/integration/resource_ownership_test.go` 使用真实 HTTP、SDK 和数据库验证：

| 测试 | 验证行为 |
| --- | --- |
| TestOptionalResourceUserIsolation | 四类范围与 A/B 用户精确隔离、同名唯一性；版本/草稿/alias/retention/Resolve 读写与回执不能跨范围；游标绑定；共享 A 创建、B 维护仍记录真实操作者 |
| TestCollectionCreateIdempotency | 首201重放200、名称规范化、异参和不同键同名冲突、8并发同键、归档/历史范围重放、当前 retention、不同 actor 及大小写/尾空格请求键、无键/null/空字符串 |
| TestCollectionCreateUnknownCommit | 真实 SQL 事务已提交但 ACK 丢失和回滚但结果不明；有键确认/重试同一 ID，无键返回未知且不认领 NULL 记录 |
| TestResourceFileAndPublicationIsolation | 个人/共享文件登记分离、读取/确认/删除/grant 不越界、个人文件只能绑定个人集合、持久引用计划保留用户归属 |
| TestResourceOwnershipMigration | 从旧模型建表和写入旧数据，再迁移；旧集合与文件保持 NULL 用户及原共享编码，不影响其他成员读取共享数据 |

`tests/sdk/resource_ownership_test.go` 验证两个 SDK 的用户范围 Header 和 context 指针快照不串用。`integration/forgetenant/resource_ownership_test.go` 验证共享、本人个人、他人个人、空值和多值 Header。`tests/unit/resource_ownership_test.go` 验证编码不碰撞、共享旧编码不变及最长多字节 ID 可存储。

## 执行结果

- `make check`：独占 PostgreSQL 17 / MySQL 8.4 容器，真实集成与 race、vet、三个入口 build、OpenAPI 重新生成一致性检查通过；测试脚本清理自己的容器。
- 补充范围/Forge 专项测试通过；`git diff --check` 通过。
- 外部真实 Forge 会话、JuiceFS STS/对象存储和现有部署未在本次重新验收；本次文件 S3/STS 协议边界使用受控本地 HTTP 后端，数据库与公开业务 HTTP 为真实实现。

## 发布顺序

先在受控窗口运行新版本迁移并部署服务，再启用带 ResourceUserID/requestKey 的调用方。旧 SDK 省略用户选择时仍访问共享范围；新客户端遇到旧服务不能移除范围 Header 或请求键重试。产生个人数据后不能回滚到忽略用户维度的旧服务。Memory 仍需独立实现自己的授权、生效、停用恢复和历史规则。
