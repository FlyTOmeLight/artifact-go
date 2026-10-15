# storage 独立部署的文件引用交接

日期：2026-10-15。状态：已确认的首期实施协议，T09已实现内部交接及单文件直接发布，草稿和完整多文件业务由T10接入；技术设计、代码结构、Spec 与 HTML 采用本协议，不保留跨域共享事务路径。授权边界沿用已确认决定：storage 只按可信租户/项目、FileID、文件状态和 S3 Actions 授权，不认识版本或草稿来源。

## 首期采用的边界

artifact 与 storage 各自提交自己的数据库事务。首期即使暂时使用同一进程和数据库，也不跨域传递 tx，不依赖一次事务同时更新两域表。首期交接即使用受认证 HTTP/JSON；combined 也通过回环 HTTP 调用，独立部署时调整端点、凭据与连接装配，保留成功条件、重试和恢复规则。具体线协议见[内部 HTTP 契约](storage-internal-http-contract-20261015.md)。

核心顺序：**先持久保留新增文件，再提交业务快照，提交时持久记录旧引用释放待办，最后由后台释放。**

这会把原来的“两域一起提交或一起回滚”改成：业务快照、Revision、编号、alias、retention 决定及成功回执仍本地原子；文件引用可能暂时多保留，但绝不能在业务已提交时缺少持有关系。失败文件保留及删除延迟是明确的结果，不宣称跨库原子。

不引入消息中间件、分布式事务协调器或通用工作流引擎。现有 artifact BackgroundTask 可靠发送释放命令；storage 仍用自己的任务回收物理对象。客户端不调用绑定、提交确认或释放接口，不增加发布流程步骤。

## 各自保存什么

artifact 保存业务清单及每项所持有的 ReferenceID，管理哪些引用属于哪个版本或草稿。storage 保存文件及引用，只认识可信调用方命名空间 ConsumerID、ReferenceID 和 FileID，不读取 artifact 表，不回调判断业务提交。

ReferenceID 是一次持有关系的身份，由 artifact 生成且永不复用。storage 中按 (ConsumerID, ReferenceID) 唯一，并固定关联 FileID 和可信租户/项目。ConsumerID 由服务身份认证产生，不能由普通客户端自报。

- 同一草稿保留同一个文件、仅修改路径或 JSON 时沿用原 ReferenceID。
- 发布版本新增自己的 ReferenceID，不转移草稿引用。
- 从版本复制草稿新增自己的 ReferenceID，不复制字节。
- 移除文件后重新加入，即使 FileID 相同，也创建新 ReferenceID。
- 失败尝试重试使用新 attemptID 和新 ReferenceID，不能复活旧引用。

storage 原本以 owner 类型和 ID 标识引用的设计需要调整：ReferenceID 成为交接和幂等身份；业务 owner 的解释与映射由 artifact 保存。storage 不依据某个 owner 是否还存在决定删除。

artifact 新增一张有明确职责的 ReferenceChange 记录，承担准备阶段的持久决策，不把普通后台任务当成唯一事务记录。逻辑键为 (ArtifactID, ActorID, OperationKind, RequestKey)，记录可信作用域、固定请求指纹、当前 AttemptID、状态、执行截止时间、完整新增 ReferenceID/FileID 集合、可信原始准备期限及必要的成功回执。固定请求指纹覆盖完整业务输入，包括路径中的 DraftID、expectedRevision、来源标识及文件清单；同一逻辑键用于不同草稿或不同输入时返回参数冲突。OperationKind 只覆盖当前实际的创建版本、创建草稿和更新草稿，不作为开放工作流类型。

每个逻辑请求最多一个活动 attempt。ReferenceChange 的旧 attempt 被中止时，在同一个本地事务中把完整释放计划写入不可变任务载荷；之后才允许同一行切换到新 attempt。任务以 Kind 加 AttemptID（删除操作使用独立删除交接 ID）去重，不用可复用的逻辑请求键覆盖旧任务。这样旧任务不会读取新 attempt 的引用集合。现有 CreateRequest、DraftCreateRequest 继续保存各自的成功业务结果；它们与 ReferenceChange 的 committed 决策在同一 artifact 事务提交。草稿更新的 appliedRevision 回执由 ReferenceChange 保存，空清单或无新增文件的更新也保持相同重试语义。

不保存每次操作的大 JSON 恢复副本：执行中的请求持有读取到的一致快照；恢复 worker 不恢复提交。终态后可清除准备阶段的明细，前提是完整释放计划已经持久交给任务；请求指纹、成功回执及判断旧 attempt 无效所需的小型记录仍保留。

## storage 的三个可信交接操作

它们是服务之间的契约，不加入普通用户 storagev1 文件操作。首期采用受认证 HTTP/JSON，combined 与分离部署共用该传输，不接收调用方 tx。

| 操作 | 契约 |
| --- | --- |
| BindReferences | 为一批 ReferenceID/FileID 建立持久引用，返回确认元数据。检查可信精确作用域、ready 未过期或仍有引用的 bound；同一次批内全部成功或全部失败。 |
| ReleaseReferences | 幂等终止所列 ReferenceID；不要求业务 owner 仍存在，也不依赖业务数据重查。每批提交在 storage 自己的事务内完成。 |
| GetReferenceStatuses | 查询引用是否 active/released，以及释放是否已完成、是否等待最后引用对应的物理删除；不返回业务模型。 |

准备阶段需要通过 storage 查询文件的确认元数据及原始 ExpiresAt，并在发出 Bind 前持久保存。查询结果不是绑定保证，Bind 必须在文件锁内重新检查状态和期限。读取可以批量实现，但不能依赖客户端声明或只依赖可能丢失的 Bind 响应取得补偿期限。

绑定与释放的每批事务都先按 FileID 排序锁文件，再处理引用；不同请求遵守同一顺序，与 storage GC 共用文件锁。大清单允许分批调用：前几批已生效、后批失败时，整个业务操作仍未成功，按照持久计划补偿。不存在要求 S3 或远程数据库参与的事务。

同一引用只有 `active → released` 一次生命周期。重复 Bind 在 active 时返回原结果；同 ReferenceID 的不同 FileID 或作用域拒绝。released 之后的 Bind 永久拒绝，不能因重试重新计数。

**Release 可以先于 Bind 到达。** 对尚不存在的 ReferenceID，storage 记录其 ConsumerID/FileID 及 released 墓碑，迟到 Bind 拒绝。这个动作没有释放实际持有关系，因此不把尚在准备期的文件直接转 deleting。不存在或跨作用域 FileID 仍须正常检查，不允许借墓碑伪造其他文件归属。

释放实际 active 引用后，仍有其他引用则保持 bound；最后一份引用释放时转 deleting 并创建 storage 回收任务。非最后引用的释放完成结果固定，不因其他引用后来删除而重新等待；最后引用需等待物理 Delete 回执才能满足原有版本删除收尾条件。未曾生效的引用墓碑直接视为释放完成。

首期 released 墓碑不自动清理、不设 TTL，即使 FileID 已 deleted 也保留。它既拒绝迟到 Bind，也保存 GetReferenceStatuses 的稳定释放回执，供 artifact 在停机或响应丢失后恢复收尾。将来清理必须先设计等价且可查询的终态回执，本期不实现。服务凭据及身份范围必须保证一个 consumer 不能释放另一个 consumer 的引用。

## 一次带文件写入如何完成

~~~mermaid
sequenceDiagram
    participant C as 客户端
    participant A as artifact
    participant D as artifact数据库
    participant S as storage
    C->>A: 创建版本 / 创建或保存草稿
    A->>S: 查询准备文件元数据与原始期限
    A->>D: 短事务保存 attempt 与新增引用计划
    A->>S: BindReferences（按计划，允许分批）
    S-->>A: 已持久保留与确认元数据
    A->>D: 最终短事务：重新校验并提交业务、成功回执、committed及旧引用释放任务
    A-->>C: 业务成功
    D-->>A: 后台读取持久释放任务
    A->>S: ReleaseReferences（可重试）
~~~

具体规则：

1. 验证身份、输入和请求指纹，先查已有成功结果。成功重试不准备新引用，不重新分配编号或移动 alias。每个尚无成功回执的新 attempt 都做 active、来源、大小及文件条件检查；只有已成功请求的重放跳过这些准备检查。
2. 读取一致的业务快照，区分新增、保留和移除的文件引用，取得 storage 的可信原始准备期限。准备阶段不占用版本编号、不移动 alias、不淘汰版本。若没有新增引用，省略文件查询与 Bind；纯 JSON 发布沿用本地事务，草稿保存仍记录请求回执，并在必要时同事务写旧引用释放任务。
3. 在 artifact 短事务内，串行检查同请求的当前 attempt，并在锁内重新检查 committed 决策、成功回执及请求指纹，不能依赖前面的锁外查询；若已成功则直接按成功重放返回。若已有仍有效的执行，返回明确的 RequestInProgress 及重试建议，不并发创建另一个 attempt。若旧 attempt 已到截止时间，先原子中止它并登记其补偿计划，再分配新 attempt。新 AttemptID、新 ReferenceID 和计划必须先持久化，之后才发出任何 Bind。
4. 在没有业务数据库锁的情况下调用 BindReferences。只给新增持有关系申请新引用；所有需要新增的引用均确认成功后才进入最终业务提交。
5. 最终 artifact 本地事务按 Artifact → 可选 Draft/来源 Version → ReferenceChange 取得锁，检查当前 AttemptID、preparing 状态、执行判定时未超截止时间及可信归属条件。草稿重新检查 expectedRevision；草稿发布重新检查指定草稿仍存在且为指定修订；来源版本复制重新检查来源仍可见且相关 Revision 与准备快照一致。不静默换用更新的来源内容。
6. 在这个事务中重新计算编号、alias 条件和 retention.Plan；写完整业务快照及每项 ReferenceID、成功去重结果、committed 决策，并为被移除或淘汰的旧引用写释放任务。新引用已在 storage 生效，因此 retention 释放旧版本时不会产生零引用空档。任一业务条件失败，业务修改全部回滚。
7. 事务提交成功后才返回业务成功。后台按任务载荷释放旧引用，不依赖可能已被删除的 DraftFile/VersionFile 数据重新拼出释放计划。

已有清单中的未变引用不需要重新 Bind。它们能被沿用的依据是最终事务仍匹配草稿 Revision；若另一页已移除或更换这些引用，当前提交必须冲突。发布草稿和复制版本都新增独立引用，不能仅依赖来源继续存在。

同一逻辑请求的成功最多一次；未成功的 attempt 结束后，客户端可用原 requestKey 和相同输入开始新 attempt。直接 JSON 由指纹固定；仅提供 sourceVersionID 的创建在新 attempt 中允许重新读取尚未成功的来源快照，但最终仍检查其 Revision。幂等不承诺冻结所有失败尝试见过的来源内容。

## 超时、失联与恢复

执行截止时间只用于 artifact 判断一个未提交 attempt 能否继续执行，不是 storage 引用的 TTL。storage Bind 一旦成功就持续保留，直到明确 Release。

worker **只负责中止和清理，不代替客户端继续发布或保存**：发现到期 preparing 后，在 artifact 数据库中以当前 AttemptID 和状态作条件更新，标记 aborted，并同事务写入全部新增引用的释放计划。最终业务提交与 abort 必须竞争同一记录；它们最多一个成功。新 attempt 不复用旧 AttemptID，旧进程即使继续运行，也不能越过最终事务检查。

没有后台接管提交，因此无需额外执行者租约继承、大 JSON 重放或业务确认回调。现有 worker 自己的任务领取租约仍用于调度；任务租约不决定业务 committed/aborted。

数据库提交连接中断时，不知道成功还是失败，必须查 artifact 主库的决策与成功记录。数据库不可用期间保留文件，不能根据超时、缓存或副本延迟认定 abort。后台同样需要取得本地终态决策才能释放。

返回明确的 CAS/业务校验失败前，要持久确认该 attempt 已 aborted；否则只能返回结果待确认的错误，客户端沿用原请求键核对。确定失败的 attempt 不会后来后台变成成功。客户端不重试时，失联 attempt 最终只被中止，不会无人操作地发布版本。

补偿计划包含**全部计划中的新 ReferenceID**，包括 Bind 响应丢失、尚未发送、结果不明的项。只释放“收到成功响应”的项会漏掉引用。消息重复和乱序通过 ReferenceID 终态处理；发送失败保留任务并重试，失败预算耗尽告警并由受控运维恢复，不能把未确认的释放标成完成。

## 并发失败后保留上传文件

分离事务后，Bind 已成功但草稿 CAS 失败是正常情况。如果立即释放最后引用，文件会进入 deleting，用户保留了本地编辑却不能使用原 FileID 另存草稿。

为保留重试机会，**对已明确 aborted 的 attempt，新增引用的补偿释放不早于该文件原始准备 ExpiresAt**；原期限已过则可立即执行。这一最早释放时间统一适用于全部计划引用，不能根据“未收到 Bind 成功”或一次查询 absent 提前释放：查询之后可能恰好有迟到 Bind 提交。到达最早释放时间后，由 storage 在同一文件锁内决定释放 active 引用或为不存在的引用写墓碑。

原始期限在 Bind 前由 storage 查询并持久保存，不以客户端参数或本次失败时间重新计算。该规则只推迟失败 attempt 的补偿，不给已成功引用添加自动过期，也不重置文件准备 TTL。客户端在窗口内以新 attempt、新 ReferenceID 重新保存；新持有关系建立后，旧 attempt 的补偿只释放旧引用。

保留到原准备期限是一项最早释放时间约束，不是精确定时删除承诺。worker 或 storage 故障可能使文件保留更久；恢复后继续补偿，不以猜测已发布状态的 TTL 换取空间。准备期很短或已过期的旧共享文件不保证还有额外重试窗口。

## 删除与回收的成功语义

删除不需要同步等待 storage。artifact 本地事务删除草稿或把版本标为 deleting，并写入完整旧引用释放任务；两者同事务提交。storage 暂时不可用不阻止业务逻辑删除。

- 草稿 DELETE 204 表示草稿已删除且释放义务已持久记录，不再表示 storage 的引用已经同步解除。
- 带文件版本删除仍先逻辑不可见；worker 发送 Release，并按 GetReferenceStatuses 等待本版本持有关系的释放结果。共享文件释放完成即可收尾，最后引用仍按原契约等待一次物理删除回执。
- 清单被隐藏、任务尚未送达期间，文件可能仍为 bound，同作用域已知 FileID 的客户端仍能申请读取。其他版本、草稿或尚待清理的 attempt 仍持有引用时同样如此。
- 最后引用实际释放后，storage 转 deleting，停止新 grant，等待 LastGrantExpiresAt 并按既有规则清理完整对象和迟到写入。只读凭证仍不延缓 GC。

删除业务内容不是立即撤销底层文件权限；这一点与已经确认的独立文件授权一致，现在还要包含释放交接的异步延迟。

## 客户端接口需要的调整

创建版本和创建草稿已有 requestKey，继续使用；草稿 PUT 增加必填 requestKey。expectedRevision 控制“能否覆盖这份草稿”，requestKey 控制“这次保存是否已经成功”，两者不能相互替代。

草稿更新成功响应为 {appliedRevision, draft}，回执记录 appliedRevision，并与当前草稿快照明确区分：初次保存返回本次提交快照；成功重试先确认 appliedRevision，再返回当前完整一致快照。如果当前草稿后来变更，draft.revision 可以大于 appliedRevision，客户端不得把旧请求内容套到当前 Revision 上继续保存。原草稿已删除则返回带原身份及 appliedRevision 的 DraftDeleted，不复活。无需永久保存每次自动保存的大 JSON 历史。

同请求仍在执行时返回 RequestInProgress（409，附重试建议）；无法从权威数据库确认提交结果时返回 OperationOutcomeUnknown（503）。这两者都要求沿用原键核对，不得按普通 RevisionConflict（409）显示“另一页已修改”。RevisionConflict 表示明确的并发失败，必须先持久中止该 attempt，再进入保留本地编辑的冲突交互。执行结果不确定时不得暗示已失败或已成功。客户端保持原 requestKey 与原参数重试；只有用户形成新的编辑意图才生成新键。已有多页面冲突处理仍适用，不自动覆盖另一页内容。

客户端仍然只登记、申请 grant、上传、确认、保存/发布；引用交接属于服务端内部协议。不增加上传会话、公开 task API 或客户端 Bind/Release 步骤。

## 故障验收

首期需要让 artifact 与 storage 使用独立数据库连接/事务进行验证，集成验收至少一组使用两个独立服务进程和两个独立数据库，不能仅以同库测试证明边界已成立。故障注入落在真实交接调用边界；数据库并发仍使用实际 PostgreSQL/MySQL。

必须覆盖：

1. intent 提交前崩溃，不得发出 Bind；intent 提交后未发 Bind 就失联，全部计划引用最终可被墓碑终结。
2. Bind 已提交但响应丢失；重试同引用不重复计数，补偿覆盖结果未知项。
3. Release 先到、Bind 后到；同 ReferenceID 永不复活，未曾绑定的 Release 不误删准备文件。
4. 多批引用只完成前几批；业务不成功，全部计划引用有持久补偿。
5. commit 与 abort 竞争；旧进程、超时进程和旧 attempt 不能越过当前决策提交。
6. artifact 最终提交成功但响应丢失、数据库暂时不可读；不能误补偿已提交引用。
7. 同一 requestKey 并发重试只存在一个活动 attempt、最多成功一次；已 aborted 后的新 attempt 不受旧释放任务影响。
8. 草稿两个页面以同 Revision 保存；只有一个成功，失败方附件在原准备窗口内可另存。
9. 来源草稿或版本在 Bind 期间改变/删除，最终提交重新检查并拒绝过时来源；不能悄悄发布另一个修订。
10. 发布新增引用与 retention 释放旧引用；新版本持有关系始终先存在，没有零引用误回收窗口。
11. 删除草稿后重启、重复/乱序发送释放、storage 长期不可用；业务已不可见，释放计划不丢，其他持有者不受影响。
12. ready 过期/最后 Release 与新 Bind 竞争；由 storage 文件锁决定成功者，deleting/deleted 不复活。
13. 失败补偿窗口不重置原 ExpiresAt；正常 committed 引用没有自动失效期限；任务失败预算不会偷偷丢弃引用交接。
14. 不同 consumer、租户或项目不能绑定/释放彼此的引用；grant 全程不需要业务来源或 artifact 回调。
15. 文件已 deleted 后，引用墓碑仍可查询；artifact 在停机或丢失释放回执后恢复，仍可确认并完成版本收尾。

## 对现有设计的替换范围

技术设计、代码结构、Spec 与 HTML 均以以下契约为准：

- 同库跨域事务、锁顺序和“一起回滚”的声明。
- StorageReference 的唯一身份与释放后重加规则；DraftFile/VersionFile 的内部 ReferenceID。
- ReferenceChange 及任务中完整、不可变的释放载荷。
- 草稿 PUT 的请求键、保存回执与结果不确定处理。
- 失败上传文件的重试保留窗口。
- 删除成功、共享文件可读期间及版本删除收尾语义。
- 各模块构造依赖、服务身份与独立部署验收。

此后首期实现应直接使用本协议，不再另外实现一个跨域共享事务的快捷路径。物理拆服务时仍需部署、身份、网络及恢复验收，但不应重写业务成功条件和引用交接流程。

## 依据与范围

业务变更与待发送释放记录在同一本地事务保存，属于[事务发件箱的可靠发送思路](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html)；接收方必须容忍重复。失败后的补偿本身也要持久、可重复执行，参见[补偿事务模式](https://learn.microsoft.com/en-us/azure/architecture/patterns/compensating-transaction)。这些资料支持可靠交接的原则；本稿的 ReferenceID 生命周期、只中止不接管提交、失败准备窗口是针对当前系统的设计选择，不是后端已经实现或验证的能力。

具体线协议与获批默认值见[内部HTTP/JSON契约](storage-internal-http-contract-20261015.md)和[开发配置](t00-configuration-operations-20261015.md)。四个内部操作采用独立Bearer服务身份和精确scope allowlist，生产/跨主机TLS；attempt固定120秒不续期。内部实现归T09，T01不提前实现引用状态机。
