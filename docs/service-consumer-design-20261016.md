# 可选用户归属与集合幂等创建

状态：Issue #2 实施契约；实际验证结果见交付记录。修订日期：2026-10-18。

本契约取代此前「user_id 必填」「复用 created_by 作为归属」「共享条目绑定创建者」的设计。artifact 管不可变版本、编号、保护、保留和通用幂等；Memory 管业务授权、生效、停用恢复、生效历史及 Run 快照。账号、登录、凭据治理不在本次范围内。

## 1. 资源归属

集合和文件分别新增可空 `OwnerUserID/ownerUserID`（Go 结构与公开 JSON 字段；数据库列保持 `user_id`，GORM 显式 `column:user_id`）。请求的 UserID 表示当前实际操作者，资源的 CreatedBy/createdBy 固定记录创建者，均与 OwnerUserID 分开。本次重命名继续映射原 user_id 列，无需额外数据迁移；从未支持用户归属的旧版本升级时，仍需新增该可空列。创建后归属固定，不新增转移或分享流程。

| tenant_id | project_id | user_id | 精确资源范围 |
| --- | --- | --- | --- |
| 必填 | 空 | 空 | 租户共享 |
| 必填 | 有值 | 空 | 项目共享 |
| 必填 | 空 | 有值 | 租户内个人 |
| 必填 | 有值 | 有值 | 项目内个人 |

空值是明确范围，不是通配符。个人查询不隐式包含共享，公共查询也不包含个人数据。旧数据新增 user_id 为 NULL，保持原有共享语义，不从 created_by 回填。

## 2. HTTP、SDK 与可信上下文

所有集合、子资源、Resolve、文件及 grant 请求统一使用以下范围选择 Header：

| Header | 规则 |
| --- | --- |
| X-Tenant-ID | 选择租户；接入边界仍需验证 |
| X-Project-ID | 选择项目；省略表示租户级 |
| X-Resource-User-ID | 选择可选资源归属用户；省略表示共享；显式空、空白和多个值拒绝 |

`artifactv1.RequestIdentity` 与 `storagev1.RequestIdentity` 新增 `ResourceUserID *string`，SDK 将非 nil 值写入 `X-Resource-User-ID`。nil 不发送 Header；不会自动使用实际操作者。字段在放入 context 时复制，不能被调用方之后修改。

资源范围只从上述可信接入路径取得，不在创建正文或列表查询中另设第二套 userID 选择参数。集合/文件响应新增 `ownerUserID`，共享响应显式为 null。文件响应同时返回独立的 createdBy。创建正文原有可选 projectID 只作与可信上下文一致性断言。

`service.RequestIdentity.UserID` 继续表示实际操作者；新增 `ResourceUserID *string` 表示经过授权的资源归属。IdentityResolver 验证调用者对所选完整范围的权限后返回两者。请求 Header 仅表达选择，本身不授予权限。中间件与写前复验均检查请求选择与 resolver 返回的 ResourceUserID 一致；旧 resolver 忽略个人范围选择时返回 403 ScopeMismatch，不能静默降为共享。

内置 Forge 接入沿用已有租户/项目成员验证：共享范围使用原规则，个人范围只允许选择已认证用户自身。需要其他业务授权规则的接入方实现既有 IdentityResolver，不增加通用账号或委托权限系统。

## 3. 查询、唯一性与文件链路

所有公开查询先限定完整范围，已知 ID、URI 或 requestKey 不绕过父集合校验。范围外详情返回既有 404，不泄露其他范围是否存在。范围选择未经授权返回 403。列表游标包含同一 scope_key，跨个人、共享或项目使用返回 InvalidCursor。

共享 scope_key 保留原值 `tenant` 或 `project:<project_id>`。个人 scope_key 为 `user:<project UTF-8 byte length>:<project_id or empty>:<user_id>`，长度前缀避免 ID 自带冒号造成碰撞；字段扩大至 160 字符。租户仍单独参与约束。ID 和请求键不转小写、不截断；只有集合名称沿用小写规范化。

```text
集合名称唯一：(tenant_id, scope_key, type, normalized_name)
集合创建幂等：(tenant_id, scope_key, created_by, request_key)
文件登记幂等：(tenant_id, scope_key, created_by, request_key)
```

版本、草稿、alias、retention 继承父集合，不重复保存可变归属。版本发布与草稿回执先验证父集合范围；同一共享集合的不同操作者仍有各自的发布键空间。

文件单独保存 user_id；读取、确认、删除、grant 使用同一 scope_key。文件发布/草稿引用要求文件和集合归属一致。ReferencePlan 的 Scope（`storage.Scope` 类型）Go 结构使用 `OwnerUserID` 字段，但持久 JSON 键保留 `userID`，确保旧版本个人计划在后台释放时仍能正确识别归属；Bind、Metadata、Release、Statuses 和后台释放均携带原固定范围；历史计划缺失 userID 按共享解释。URI 已包含固定集合 ID，不改格式，Resolve 仍验证完整范围。

## 4. 集合幂等创建契约

`POST /v1/artifacts` 与 `CreateArtifactRequest` 增加可选 `requestKey`。SDK `CreateArtifact` 返回类型不变。

```json
{"type":"memory","name":"entry-123","versionScheme":"integer","requestKey":"operation-123-create"}
```

非空键至多 64 字符，不含 NUL、CR 或 LF；保留大小写和空格比较语义，与现有版本请求键保持一致。省略、null 或空字符串采用旧的非幂等行为，写入 SQL NULL，不参与幂等查询。Memory 必须提供稳定非空键。

规范参数为 type、小写 name、versionScheme、规范 initialVersion；省略 initialVersion 等同相应默认值。可信资源范围和实际 actor 属于键空间，不再拼入参数 hash。集合自身的不可变字段和 request_key 构成成功事实，无独立回执表、Pending 状态或失败记录。

| 情况 | 结果 |
| --- | --- |
| 首次成功 | 201，返回集合 |
| 同 actor、完整范围、键与规范参数 | 200，同一 ID、当前状态与当前 retention |
| 同键合法异参 | 409 IdempotentParameterMismatch |
| 不同键、同范围同 type/name | 409 ArtifactNameConflict，不按名称认领 |
| 不同 actor 或不同范围的相同键 | 独立请求，仍受各自名称约束 |
| 归档后或已授权历史范围重放成功键 | 200，保留归档状态；不新建、不取消归档 |
| 历史范围首次创建 | 403 ScopeInactive |
| 提交结果无法确认 | 503 OperationOutcomeUnknown |

先验证当前身份和参数；非空键查成功记录后，才判断是否允许首次创建。集合行与键在同一事务落库，唯一约束处理并发。同键异名竞争的败方回滚本次插入，不能留下第二个集合。

遇到唯一冲突或 COMMIT 结果不明，退出失败事务后，在至多 2 秒的独立 context 中从主库读取原键，并从一致读事务读取集合与 retention。查到后确认参数再返回；COMMIT 仍不明或确认查询失败返回 503。明确名称冲突且没有匹配成功键返回 409。

无键请求不查询 NULL 键。无键提交结果不明同样返回 503，但不承诺安全重试；调用方应检查当前资源，而不能认为原键可以恢复。HTTP 响应丢失后的有键重试可以恢复同一结果。

## 5. 迁移与发布

使用现有 AutoMigrate 入口新增可空字段、集合创建键唯一索引及 scope_key 长度，保留原共享 scope_key。PostgreSQL 和 MySQL 均验证 NULL 请求键允许多个非幂等集合；共享名称仍由非空 scope_key 唯一约束保护。MySQL 沿用 utf8mb4_0900_bin，保证 ID 和键的大小写、尾空格语义。

迁移应在受控发布窗口执行，不修改已有 user 归属。先迁移并部署支持新 Header/字段的服务，再启用个人范围和 requestKey 客户端；旧服务不理解新能力时不得去掉 Header/requestKey 重试。SDK、OpenAPI 和 README 同步发布。新个人数据产生后不能回退到忽略用户范围的旧服务。

## 6. Memory 消费

个人条目设置资源 user_id；ProjectShared 留空。ArtifactBinding 固定 store、tenant/project/可选 user 和集合 ID。A 创建共享条目、B 更新、A 退出项目、B 停用并恢复，始终访问项目共享范围；每次先检查当前维护者的 Memory 权限，实际操作者单独审计，不借用 A 的身份。

集合创建键和版本发布键由稳定 operationID 派生。幂等键包含实际 actor，同一操作重试须保留原 actor；换维护者是新操作，不认领原操作回执。Memory 的生效历史仅展示本地 Applied 操作对应版本；artifact 仍保存完整通用版本历史。

## 7. 验收条件

- PostgreSQL/MySQL 上四类范围创建、列表、修改相互精确隔离，同名可跨范围并存，同范围重复拒绝。
- 个人 A/B、个人/共享、tenant/project 不能通过 ID、URI、游标、发布键访问其他范围；共享 A 创建、B 维护时 createdBy 保留 A、版本实际 actor 为 B。
- 文件读取、确认、删除、grant 和版本/草稿引用均验证归属；后台释放保留个人范围。
- Header 未授权、被旧 resolver 忽略、空字符串/多值均拒绝；SDK 身份快照保持独立；Forge 只接受自己的个人范围。
- 同键并发、异参、大小写/尾空格、不同 actor/范围、归档/历史重放和当前 retention 通过真实 HTTP/SDK 验证。
- 真实数据库故障注入覆盖已提交但 ACK 丢失、回滚但 ACK 不明、有键原参数重试和无键不查询 NULL 回执。
- 旧模型数据迁移后 user_id 为空、旧 scope_key 与共享可见性不变；两种数据库都建立有效唯一约束。
- 运行 make check，覆盖双库集成、race、vet、build 和 OpenAPI 一致性；数据库 skip 不算验收通过。外部 Forge/JuiceFS 环境验证另列，不冒称已运行。
