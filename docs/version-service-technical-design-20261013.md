# 通用版本管理服务技术设计

设计基线日期：2026-10-13；storage 职责调整：2026-10-15。本文是已确认的实施规格；当前仅开始T01，后续行为不代表已经实现。

代码组织与 Module Interface 见[代码结构设计](codebase-design-20261014.md)。首期即采用[独立事务的引用交接协议](storage-reference-handoff-design-20261015.md)：先持久 Bind，再提交 artifact 业务及释放任务，后台可靠 Release；同进程也不跨域共享事务。

本服务为租户级和项目级资源提供统一的版本管理。数据库保存不可变的 JSON 内容快照以及可选文件清单；整数版本和语义化版本由服务自动分配。alias 表达可移动引用，保留策略控制版本生命周期。PostgreSQL 或 MySQL 是编号、资源归属、幂等和任务状态的事实来源，多个 server-go 副本共享数据库与文件存储。

## 需求与设计结论

| 要求 | 设计 |
| --- | --- |
| 物料类型 | 调用方创建集合时传入 Type，如 skill、model；类型进入物料持久引用；storage 对象路径不包含物料类型 |
| 纯数字自增版本 | 集合内按成功发布顺序分配 1、2、3 |
| 语义化自增版本 | major、minor、patch 三段，创建时选择递增项，默认 patch |
| 无文件版本 | JSON 完整快照直接存入数据库 |
| 带文件版本 | 同一版本模型关联零到多个文件，上传完成后发布 |
| 版本保留 | 数量、年龄、最低保留数量、删除保护、alias 引用保护 |
| alias | 集合内唯一、可移动，使用修订号检查并发修改 |
| 多副本并发 | 集合行锁、唯一约束、数据库幂等记录、持久化任务租约 |
| 数据库 | PostgreSQL 16 及以上、MySQL 8.4 InnoDB 作为首批验证基线 |
| 文件存储 | 统一使用 S3 协议，通过限权短期凭证让客户端直传；本地部署同样使用通过存储契约测试的对象存储 |
| 服务框架 | server-go，显式装配数据库、存储与业务对象 |
| Redis | 可选缓存与任务分发组件，不承担版本分配 |
| 租户集成 | 接收可信身份与作用域 context，暂不接入操作权限判定 |

版本管理服务负责内容快照、编号、引用和生命周期。租户服务负责企业、账号、成员、团队、项目及授权合并。GitLab、Casdoor 的映射继续由租户服务维护。

第一版采用完整快照、草稿与版本按 FileID 复用文件、普通三段正式版本。分支合并、差量存储、预发布版本、按内容哈希自动去重与跨项目迁移不属于本版契约。

## 开源设计参考

| 项目 | 已核实机制 | 本服务采用的设计 |
| --- | --- | --- |
| MLflow | 整数版本、alias、元数据与文件分离；Prompt Registry 可保存无文件内容 | 统一集合、快照与引用；以明确的 payload 字段保存纯数据库内容 |
| Harbor | 保留规则、不可变保护、预览；逻辑删除与文件 GC 分离 | 策略解释、发布和回收分工、可恢复文件删除 |
| zot | Go 实现、local 与 S3、保留与垃圾回收 | 集中的文件存储接口 |

MLflow 的创建版本模型适合借鉴，但本服务使用稳定集合行分配编号。Harbor 与 zot 面向 OCI，不能直接视为本服务的纯数据库快照或双数据库实现。

来源：[MLflow Prompt Registry](https://mlflow.org/docs/latest/prompts)、[MLflow Backend Stores](https://mlflow.org/docs/latest/tracking/backend-stores/)、[Harbor 保留策略](https://goharbor.io/docs/main/working-with-projects/working-with-images/create-tag-retention-rules/)、[Harbor GC](https://goharbor.io/docs/main/administration/garbage-collection/)、[zot 存储](https://zotregistry.dev/v2.1.17/articles/storage/)。

## 领域模型

领域术语统一见 [CONTEXT.md](../CONTEXT.md)，下表说明设计中的主要对象。

| 名称 | 定义 |
| --- | --- |
| Artifact | 被版本化的物料集合。类型、归属、编号模式和计数器由它持有 |
| Version | 一次成功发布的快照，包括 JSON 内容和可选文件清单 |
| Draft | 一个Artifact中的独立可编辑JSON与文件清单草稿，同集合可有多份；独立于Version，不分配编号 |
| Alias | 在同一个 Artifact 内指向一个可见 Version 的命名引用 |
| VersionFile | 版本的逻辑路径、StorageFile 引用及确认元数据快照，由 artifact 管理 |
| StorageFile | 存储文件的实际位置、上传确认、引用状态及物理生命周期，由 storage 管理 |
| DraftFile | 草稿逻辑路径、FileID及确认元数据，随草稿Revision一起更新 |
| StorageReference | ConsumerID/ReferenceID 对 FileID 的一次持有关系或释放墓碑，由 storage 拥有 |
| ReferenceChange | artifact 保存的引用准备计划、当前 AttemptID、提交/中止决策及保存回执 |
| StorageTask | storage 自己的持久物理回收任务，不读取 artifact 表 |
| RetentionPolicy | 对一个集合生效的保留规则 |
| CreateRequest | 已成功发布请求的参数指纹和结果身份 |
| DraftCreateRequest | 已成功创建草稿的请求指纹与DraftID，草稿硬删后仍保留 |
| BackgroundTask | 需要在事务外执行并可重试的持久任务 |

tenant 服务的 tenants、users、memberships、teams、projects 和 grants 保留原有含义。租户记录自身的版本号与物料版本号分别管理。

### 必须保持的关系

- 一个 Artifact 属于一个租户，可以进一步属于该租户的一个项目；OwnerUserID 有值表示该范围内的个人资源，空值表示共有资源。
- 一个 Artifact 有且只有一个创建时确定的 Type，全部版本继承集合的类型。
- 一个 Version 只属于一个 Artifact。
- storage 文件登记不要求 Artifact 存在，也不带 ArtifactID、Type 或逻辑路径。
- 一个 StorageFile 可被同一精确 scope 内的多个草稿、版本乃至不同 Artifact 引用；同一清单中不允许重复 FileID 或逻辑路径。VersionFile 是发布时建立的清单快照，DraftFile 是可编辑清单，StorageReference 以独立 ReferenceID 记录各次持有关系。不同租户、项目、个人归属及个人/共享范围之间均不能复用。
- alias 目标必须与 alias 属于同一个 Artifact。
- 清理全部版本不删除 Artifact 的编号状态。
- 平台账号可以属于零个或多个租户。用户与项目之间通过授权建立访问关系。

### 物料类型

创建集合时调用方必填 type，例如 skill、model、dataset、prompt。Type 是集合的稳定业务分类，服务接受开放的类型标识，不硬编码这些示例为封闭枚举，也不按类型增加不同的版本发布流程；调用方负责各类型内容的业务含义。

Type 仅接受 1 至 64 个字符的小写 ASCII 标识，以字母开头，后续字符为字母、数字、连字符或下划线。类型创建后固定，版本不能覆盖集合类型；物料持久引用使用该规范值。类型参与资源定位时必须与数据库记录一致，不能只凭请求路径中的类型判断归属。

不同类型拥有独立的集合命名空间。同一租户、同一作用域内允许 skill/demo 与 model/demo 同时存在；同一类型内名称仍须唯一。ArtifactID 继续全局唯一，类型不会改变已有集合按 ID 定位的方式。

## 请求身份与作用域隔离

### 资源作用域

完整资源作用域由租户、项目和可选归属用户组合确定，共四类精确范围。2026-10-18 新增可选用户归属后，作用域从两类扩展到四类：tenant 共享、project 共享、tenant 个人、project 个人。NULL 不是通配符，共享范围不代表匿名公开；仅经过 IdentityResolver 授权的已认证用户才能访问对应范围，Forge 仍只校验租户成员和项目归属，不查询项目 grants，不引入新的权限系统。

| 作用域 | 适用资源 | 持久字段 |
| --- | --- | --- |
| 租户共享 | 企业共享配置、提示词、模板 | TenantID 有值，ProjectID 为 NULL，OwnerUserID 为 NULL |
| 项目共享 | 项目数据集、模型、配置 | TenantID、ProjectID 有值，OwnerUserID 为 NULL |
| 租户内个人 | 租户内个人物料 | TenantID 有值，ProjectID 为 NULL，OwnerUserID 有值 |
| 项目内个人 | 项目内个人物料 | TenantID、ProjectID、OwnerUserID 均有值 |

完整的资源归属契约、精确范围编码、Header 选择器和迁移策略见[可选用户归属与集合幂等创建](service-consumer-design-20261016.md)。

第一版的 Forge 接入层要求有效用户和 active 租户成员资格；选择项目时只核对项目属于该租户，不查询项目 grants 或 read/write/manage。平台 operator 不能绕过租户准入。artifact 领域层不校验读取、发布、alias、版本保护、保留策略和删除的角色与操作权限。在可信请求作用域内，调用方可执行各类操作，但必须满足资源状态、保护、引用与并发控制等业务条件。当前 Forge 接入只接受 active scope，历史 deleted scope 暂拒绝；核心服务的 OwnershipDeleted 注入契约保留给具备权威历史授权的接入方。

### 请求 context

接入方 Middleware 验证身份并建立可信的请求作用域，写入以下字段。HTTP API 路径不包含 TenantID 或 ProjectID；不能把未经验证的客户端 Header 直接当作可信 context。作用域的取得和验证由接入方负责，artifact-go 不反调租户服务判定操作权限。

| 字段 | 约定 |
| --- | --- |
| UserID | 必填，已验证的当前操作者，用于审计和创建人记录，不作为资源所有者限制 |
| TenantID | 必填，本次请求的租户 |
| ProjectID | 可选，本次请求的项目；为空仅表示租户直属范围，不表示所有项目 |
| ResourceUserID | 可选，资源归属用户选择；为空表示共享范围，显式空值拒绝。2026-10-18 新增，与 Artifact/StorageFile 的 OwnerUserID 对应 |

context 不传 Permissions、角色或管理员标志。接入方建立新建集合所用的作用域时，负责确认租户存在、状态有效，以及项目属于该租户。每次请求仅使用一个确定的完整资源作用域（tenant 共享/project 共享/tenant 个人/project 个人）。

artifact-go 执行以下检查：

- 请求必须具有完整有效的身份和作用域 context。
- 目标 Artifact 的 TenantID、ProjectID、OwnerUserID 必须与 context 一致；集合列表先按该完整作用域过滤，再排序、分页。
- 创建集合从 context 写入归属（含 OwnerUserID）；请求体若提供 ProjectID，必须与 context 一致，不能覆盖作用域。
- 版本、草稿、alias 和版本文件清单必须属于目标集合；持久 URI 中的归属和类型必须与 context 及数据库记录匹配。storage 文件单独检查其精确租户/项目/用户归属，发布绑定再核对与集合相同。
- 资源状态、Protected、alias 引用、Revision、待绑定文件有效期等业务条件继续检查。
- 重试和缓存命中仍先检查身份及作用域，不能通过幂等结果绕过隔离。

后台任务使用服务自己的存储凭据，按已保存的策略与目标资源状态执行，不依赖版本创建者的成员关系。对象存储临时凭证仍限定对象、操作和有效期；暂缓业务操作权限校验不改变存储限权要求。

### 归属对象删除后的物料

租户或项目删除不触发版本服务级联删除、自动解除保护或移除 alias。Artifact 保留原 TenantID、ProjectID 及编号身份，已有版本、文件和引用继续遵循自身生命周期与保留策略；既定回收任务继续执行。

归属对象删除后不能继续登记文件、续发上传授权或发布新版本。接入方负责落实归属状态的检查与删除联动，不能因为暂缓操作权限判定而省略该生命周期约束。成功发布请求的重试仍先检查身份与历史作用域，再按幂等规则确认结果；已签发存储凭证可能继续有效到期，其产生的未发布对象按 storage 文件生命周期回收。

遗留物料维护仍使用包含原 TenantID、ProjectID 的可信 context，artifact-go 检查历史归属一致性；显式删除仍须先解除普通 alias 引用和显式保护。由谁进入历史作用域、如何区分租户管理员与平台管理员，随租户权限模型另行确定，不作为本版已实现的权限保证。

## URI 与文件位置

### 持久引用格式

~~~text
租户级集合：
artifact://<tenantID>/artifacts/<artifactType>/<artifactID>

项目级集合：
artifact://<tenantID>/projects/<projectID>/artifacts/<artifactType>/<artifactID>

固定版本：
<artifact-uri>/versions/<versionID>

版本文件：
<artifact-uri>/versions/<versionID>/files/<logicalPath>

alias：
<artifact-uri>/aliases/<alias>

alias 文件：
<artifact-uri>/aliases/<alias>/files/<logicalPath>
~~~

示例：

~~~text
artifact://t01/artifacts/skill/a01/versions/v01
artifact://t01/projects/p01/artifacts/model/a02/versions/v02
artifact://t01/projects/p01/artifacts/model/a02/versions/v02/files/weights/model.bin
artifact://t01/projects/p01/artifacts/model/a02/aliases/production
~~~

固定版本 URI 指向固定的 JSON 快照及文件清单；alias URI 的目标会随 alias 更新而变化。解析 alias 时同时返回实际 VersionID、编号和固定版本 URI。

固定版本 URI 保证版本身份、JSON 快照和已登记文件清单不变，不承诺外部文件字节不变或永久可读。客户端发布后修改对象由客户端自行负责；服务不检测、恢复或重新核验这些修改。下游保存或使用 URI 不会自动建立保留义务，服务不登记下游引用。调用方需要持续读取某个版本时，应申请由集合管理者显式保护该版本；未保护的版本可以按删除规则清理，调用方应接受其随后无法读取。

使用租户服务的稳定 TenantID 和 ProjectID。租户 slug、资源名称、GitLab 映射、用户名与团队名称不进入持久引用。物料 ID 与版本 ID 由版本服务生成，采用适合 URI 的规范标识；对接时将租户服务的规范 ID 字符契约纳入接口契约。

可信身份的TenantID必须匹配`^[A-Za-z0-9_-]{1,64}$`，保留大小写，不做slug转换；TenantID位于artifact:// URI的authority，因此拒绝`/`、`:`等分隔字符，避免生成无法解析或改变归属层级的URI。UserID及非空ProjectID继续使用opaque标识：最多64个Unicode字符、无首尾空白、不含NUL/CR/LF；ProjectID逐路径段编码，不套用TenantID字符集。缺失ProjectID表示租户直属范围。

URI 是服务定义的资源引用，由 SDK 或 ResolveURI 接口解析。它不是可直接下载的 HTTP 地址。逻辑文件路径按 URL 路径段编码，保留路径层级，禁止绝对路径、点路径段和反斜杠；路径区分大小写。

HTTP API 路径与持久资源引用采用不同的定位方式：API 路径只包含业务资源 ID，租户和项目作用域从请求 context 取得；服务生成的 artifact:// URI 包含 TenantID、可选 ProjectID 和 Artifact.Type；storage 的 ObjectKey 只表达文件身份，不依赖集合类型或版本。持久 URI 中的归属信息不能覆盖请求的租户上下文。

### 物理位置

一个租户对应一个 bucket，同租户的直属资源和项目共用空间。租户系统触发创建，当前由同进程的 storage 模块提供内部管理入口；文件登记和发布不隐式创建 bucket。可信 TenantID 按固定命名 v1 从 `t_<24hex>` 派生 `tenant-t-<24hex>`，无租户映射表，不能从请求接收任意 bucket。

StorageFile 独占保存 BackendID、Bucket、ObjectKey，后续授权、确认、回收始终使用原位置。一个配置的 BackendID 对应 S3/STS 连接；第一版不建多后端注册表。文件 ID 与 key 永不复用，key 使用 `files/<fileID>`（如有部署固定前缀则包含该前缀），不包含 ArtifactID、Type、版本号或逻辑路径；scope 以存储记录为准。VersionFile 只保存 FileID、内部 ReferenceID 与逻辑清单元数据，不复制物理位置或临时凭证。

更换 bucket 或连接身份属于显式迁移，不能改变规则后重新解释历史位置。删除集合或项目不删除整个租户 bucket。bucket 未准备或不可访问只阻止文件操作，不影响 JSON 版本。

### URI 解析过程

1. 解析 scheme、租户、可选项目、物料类型、ArtifactID 和引用类型。
2. 确认引用中的 TenantID、ProjectID 与请求 context 一致，再按 ArtifactID、Type 和可信完整作用域校验集合。URI 不包含归属用户，集合的 OwnerUserID 仍须与 context.ResourceUserID 精确一致，已知 URI 不能绕过个人归属隔离。
3. 检查目标资源状态及本次操作的业务条件。
4. 查询指定版本，或在一次数据库读取中解析 alias 到可见版本。
5. 返回 JSON 内容、固定引用和文件清单。
6. 客户端可用解析结果中的FileID调用 POST /v1/storage/grants，scope选download或显式s3:GetObject。storage独立检查可信租户/项目/归属用户及文件可读状态后签发STS，不接收业务来源、不回查版本或草稿。Resolve只返回元数据；取得FileID不是身份授权证明。

用户修改 URI 中的租户、项目或物料类型部分，不会改变数据库中的真实归属和类型。

同一用户切换租户或项目后，解析持久引用也需要在对应的可信请求作用域中进行，不能仅凭 URI 自动切换当前作用域。

## 编号规则

### 整数模式

首版固定为 1，后续按集合内的成功发布顺序加一。整数模式请求不接受 bump，省略该参数即可。失败事务回滚计数器，删除版本不回退编号。存储类型为有符号 BIGINT，达到上限时返回明确错误。

### 语义化模式

首版使用集合创建时指定的 InitialVersion，默认 1.0.0。首版 bump 可省略或为 patch；major、minor 在没有已发布基线时返回参数错误。后续创建根据以下规则分配编号：

| 当前值 | bump | 结果 |
| --- | --- | --- |
| 1.4.7 | patch 或省略 | 1.4.8 |
| 1.4.7 | minor | 1.5.0 |
| 1.4.7 | major | 2.0.0 |

minor 增长时 patch 归零，major 增长时 minor 与 patch 归零。调用方选择变化等级，服务不推断业务兼容性。正式版本规则遵循 [SemVer](https://semver.org/)。

本版不接受创建请求直接指定编号。两种模式的对外 Number 均为字符串，每个版本另有内部 Sequence。排序、分页与保留使用 Sequence，语义化版本额外保存三个数值字段。

集合创建时验证 InitialVersion：整数模式必须为 1，语义化模式必须为合法三段正式版本。VersionScheme 与 InitialVersion 在集合创建后固定。即使所有版本被删除，后续发布仍从集合计数器继续。

## 带文件的工作草稿

每个 Artifact 可以有多份 Draft，各自具有永不复用的 DraftID 和 Revision。草稿保存 Payload、Annotations 和零到多个文件引用；JSON 与清单共同组成一个可编辑快照，共用一个 Revision。草稿不分配 Number/Sequence，不进入 latest、alias 或版本保留计算；无自动过期、历史修订或分支合并。已保存的文件引用持续保留，直到用户明确删除草稿或从草稿移除文件。

Draft 模型包含 ID、ArtifactID 外键及普通索引、Payload、Annotations、Revision、CreatedAt、UpdatedAt；初始 Revision=1。DraftFile 保存 DraftID、LogicalPath、FileID、内部 ReferenceID 以及确认元数据，唯一约束为 (DraftID,LogicalPath) 和 (DraftID,FileID)。FileID 不建立跨域外键，引用有效性由业务提交前独立完成的 storage.BindReferences 及持久交接决策保证。GET 返回 JSON 和 files 清单，刷新可恢复已保存附件。

草稿有五个 CRUD 操作：POST集合路径首次创建201、成功请求重试200；GET集合路径列表200；GET单草稿200；PUT单草稿200；DELETE单草稿以 query expectedRevision 检查并发，成功204。每个操作检查精确scope与集合归属；首次CreateDraft和UpdateDraft要求active归属及未归档集合。DeleteDraft作为历史清理允许归档集合或已删除的租户/项目，只校验可信历史精确scope、Artifact/Draft归属与expectedRevision；不要求active，也不放宽创建或编辑条件。列表ID升序，limit默认50、范围1–200，游标绑定scope及ArtifactID。已删除草稿 GET/未成功 PUT/首次发布404，已成功 PUT 重放按保存回执返回410，不提供includeDeleted，不以PUT重建。附件通过统一storage grants接口按FileID申请读取权限；未保存进草稿但已确认ready且未过期的文件同样可以预览。

CreateDraft 必填requestKey（最多64字符），与创建来源一起提交。有两种互斥来源：直接提供对象 payload/annotations 和可选 files（省略为[]），或提供 sourceVersionID。来源版本必须在同集合且ready，复制其JSON和完整清单，并为新草稿增加文件引用，不复制字节。直接字段显式null拒绝。UpdateDraft 必须同时提供 requestKey（最多64字符）、payload、annotations、files 和正数 expectedRevision，完整替换；files:[] 表示移除所有附件，漏传files返回400，防止调用方意外清空。文件项仅为path/fileID，路径规则与版本一致，重复path/FileID拒绝，草稿同样受MaxFilesPerVersion限制。

CreateDraft 的幂等由独立 DraftCreateRequest 保存，字段为 ArtifactID、ActorID、RequestKey（联合唯一）、RequestHash、DraftID、CreatedAt。ArtifactID 引用 Artifact，DraftID 不建立到 Draft 的外键，硬删草稿不会删除成功结果。它与版本 CreateRequest 分开，避免其 Version 外键及发布语义混入草稿。新增引用先由 storage 独立持久绑定；草稿、清单、成功记录与 ReferenceChange.committed 在 artifact 本地事务提交。失败中止 attempt 并持久登记全部计划引用的补偿，不能跨库回滚 Bind。

请求指纹使用原始创建意图：直接来源按JSON归一与path排序清单编码；复制来源使用sourceVersionID，不读取当前来源内容重新计算；requestKey和身份不入指纹，省略files与[]等价。先验证身份、历史精确scope、请求结构和整包大小，再锁Artifact查询成功记录。相同键异参409；同参且草稿仍存在，按Artifact→Draft锁返回原DraftID的当前完整快照及Revision，HTTP200，不重新复制来源、不新增引用、不检查当前active、来源版本或文件准备期限。原草稿已删除则410 DraftDeleted，携draft:{artifactID,id}，不返回内容、不复活。每个没有成功回执的新 attempt 都检查active、来源、字段大小及文件条件，生成新DraftID并返回201。

客户端在首次发送前保存requestKey及对应请求；响应丢失沿用原键与原参数。200返回的是当前草稿，可能已被其他页面编辑，不能将其Revision套到本地旧内容上；保留本地编辑并按已有冲突交互核对。成功结果记录不自动过期，避免旧创建重试生成另一份草稿。创建幂等仅保证一次创建意图，另存新副本必须使用新requestKey。

GetDraft 在短事务内按Artifact→Draft加锁，锁内读取Payload、Annotations、Revision和DraftFile，并组装完整响应；写事务使用同一顺序，不能仅依赖READ COMMITTED下多次无锁SELECT。Create/Update 首次成功响应在写事务锁内组装本次快照，提交成功后返回，不能提交后分别重读；成功重放按回执规则读取当前一致快照。列表返回DraftSummary：id、artifactID、revision、createdAt、updatedAt，不包含payload、annotations或files，仍不保证跨页快照。单草稿读取与PUT/DELETE交错必须只返回完整修订或404。

只有同scope的 ready 且未过期文件，或仍有有效引用的 bound 文件，才可保存进草稿。uploading 文件不能进入持久清单：客户端显示上传进度，完成确认后再保存；未保存的准备文件到期仍会回收。替换文件须准备新FileID，再条件保存清单；改逻辑路径但保留FileID不改变引用。仅凭内容相同不自动去重；客户端显式选择已有FileID才复用。

Create/UpdateDraft 先读取一致业务快照并计算引用差集，再持久记录 attempt 和新增引用计划；在不持 artifact 锁时调用 BindReferences。最终本地事务按 Artifact → 可选 Draft/来源 Version → ReferenceChange 加锁，重新检查 Revision、当前 AttemptID、preparing 与截止时间，原子更新 JSON、DraftFile、Revision、成功回执及旧引用释放任务。不变引用保留原 ReferenceID；路径变化不重新绑定。失败只回滚本地业务，并持久中止、补偿新增引用。没有新增引用时省略 storage 查询和 Bind，保存回执与释放任务仍同事务提交。

DeleteDraft 在 artifact 本地事务检查 expectedRevision，硬删 Draft/DraftFile 并写入含完整 ReferenceID/FileID 的不可变释放任务，204 表示释放义务已持久记录，不表示 storage 已完成释放。storage 故障不阻止逻辑删除；任务不依赖已删除清单重建载荷。发布版本及成功回执不依赖 Draft 存续。

UpdateDraft 的幂等由 ReferenceChange 保存：逻辑键为 (ArtifactID,ActorID,OperationKind,RequestKey)，固定指纹包含路径 DraftID、expectedRevision 和完整替换内容。成功响应为 {appliedRevision, draft}；首次成功返回 appliedRevision 及该次提交的 draft；成功重放返回原 appliedRevision 和当前完整 draft，draft.revision 可以更高。原草稿已删返回410 DraftDeleted及原身份、appliedRevision；不恢复内容。无成功回执时才检查 active、当前 Revision 和文件条件。同键异参409；请求进行中409 RequestInProgress，结果无法从主库确认503 OperationOutcomeUnknown，均沿用原键原参核对。只有确认 attempt 已 aborted 才返回确定的 RevisionConflict。

CreateVersion 的 draftID/expectedDraftRevision 必须成对出现，正数Revision，与直接payload/annotations/files字段存在互斥（即使files:[]也拒绝）。首次发布读取指定草稿一致修订，先为新版本持久绑定独立 ReferenceID，再于最终本地事务重查草稿修订并复制 JSON 和完整清单。发布不修改草稿内容或Revision、不删除草稿、不转移其引用。同一修订用不同requestKey可生成不同版本，文件可继续复用。

发布先锁Artifact并检查CreateRequest，已成功的幂等请求先确认历史结果或410，再检查首次发布的active状态、Draft存在和Revision。指纹使用来源、draftID、expectedDraftRevision与发布参数，不按当前草稿重新计算。成功后草稿修改或删除不破坏重试。本地事务失败回滚编号、清单、alias和保留操作；已绑定引用走持久补偿，不推断远程回滚。保存JSON沿用json.Number。

### Web 编辑交互契约

“从已发布版本创建草稿”和“另存为新草稿”显式创建新DraftID；“继续编辑”打开已有DraftID。页面URL须包含ArtifactID和DraftID，刷新、恢复标签页、复制链接或重复打开均读取同一草稿，不能在页面加载时自动创建。客户端在已有DraftID上自动保存；服务端不管理标签页、编辑会话或单人占用锁。

同一草稿的每个页面保留与实际JSON及文件清单对应的基线Revision及未保存内容；自动保存按页面串行，保存响应只更新其对应基线，不丢弃保存等待期间的新本地编辑。不能仅把其他页面的新Revision同步过来而保留旧内容。RevisionConflict（409）时暂停自动保存和发布、保留本地内容，提示“这份草稿已在其他页面修改”，提供查看最新内容或把本地修改另存新草稿；未经用户选择不能覆盖最新内容或自动刷新丢弃本地编辑。404时按草稿已删除/不存在处理，同样保留本地内容并允许另存新草稿；另存时若文件已进入回收，须重新上传，不能复活旧FileID。每次保存先持有 requestKey 与对应参数。网络失败、RequestInProgress 或 OperationOutcomeUnknown 沿用原键原参核对，不提示另一页修改、不推进 Revision 或直接发布。成功重放的 appliedRevision 确认本次保存结果；返回当前 draft 必须整体采用或比较，不能把它的新 Revision 套在旧本地内容上。失败附件的补偿不早于原准备 ExpiresAt，不重置该期限；在窗口内可新建 attempt 重试或另存，已回收文件须重新上传。

发布前如有待加入附件或未保存修改，先等待上传确认，再串行条件保存JSON与完整文件清单；已保存且本地无改动时可直接使用对应DraftID/Revision。只有已确认保存的修订才调用CreateVersion；有冲突或保存失败停止发布，不能发布服务端另一页的当前内容。发布期间的新编辑不改变本次已选修订；其他页面抢先更新导致发布Revision冲突时，保留本地内容并进入冲突处理。发布请求重试沿用原requestKey和已选修订，不自动替换为最新草稿。

测试仍复用已确认HTTP/SDK与真实双库seam：同集合多草稿相互隔离、五操作及游标、同Draft两个客户端的Revision竞争、不同Draft独立编辑、复制JSON与清单并保留引用、删除旧ID不可复用；发布和编辑/删除竞争只得到指定修订完整快照或409/404；成功发布后编辑/删除仍幂等，版本删除后仍410；失败不推进编号或改变alias，草稿不影响latest/保留。用两个独立SDK客户端验证服务端交互，Web接入方验证暂停自动保存、保留本地、另存、刷新不创建和先保存再发布。交互约定不把Web前端实现加入本仓库交付。


## 无文件与带文件版本

### 纯数据库内容

调用方提交完整 JSON payload，服务将其保存到 Version.Payload。读取指定版本返回当时的值，更新内容创建新版本。API 与 SDK 使用标准 JSON 类型，不暴露 GORM 类型。

JSON 是值级快照，不保证输入文本的缩进和键顺序。需要保留原始字节时，将内容作为文件上传。草稿和版本共用下述明确大小契约。

### 内联 JSON 限制与显式大 JSON 文件

| 配置 | 默认值 | 范围与超限结果 |
| --- | --- | --- |
| MaxPayloadBytes | 256 KiB（262144字节） | 单个payload超限413 PayloadTooLarge |
| MaxAnnotationsBytes | 64 KiB（65536字节） | 单个annotations超限413 AnnotationsTooLarge |
| MaxRequestBodyBytes | 8 MiB（8388608字节） | 整个JSON HTTP请求体超限413 RequestBodyTooLarge |

前两项计量经json.Compact去掉字符串外空白后的UTF-8 JSON字节数，保留输入字符串转义和数字词法，不经float64转换或按字符数计量；省略默认{}计2字节。整包限制按接收的JSON请求体字节计量，包含空白、字段名及files数组，读取时有界限制，不能先无界读入内存再判断。大小等于上限允许，严格大于才拒绝。HTTP/SDK沿用同一契约；服务配置使用正数、所有副本一致，前置代理不得配置比本契约更小的有效请求体上限。

首次创建或更新草稿、首次直接发布、从版本复制草稿、首次发布草稿，以及修改版本annotations，均检查本次要写入的字段上限。复制/草稿发布即使请求只有ID，也检查来源内容；超限不创建草稿或版本、不推进Revision/编号；大小检查先于 Bind，不建立新引用。成功幂等命中不按当前草稿/来源/配置上限重新检查已持久内容，但当前请求仍受整包大小与结构限制。读取现有内容不因降低写入上限而失效。无请求体的读取与删除不适用请求体上限。

来源复制的字段大小按数据库读出的JSON表示compact后计量；不承诺持久化往返保留字符串转义或数字文本写法，也不使用float64中转。

MaxFileBytes 是storage独立的、部署必填的正数字节上限；普通附件和显式JSON文件都适用，不受256 KiB内联限制。文件登记超过该限按既有FileLimitExceeded逐项拒绝；实际对象大小必须等于声明且符合上限才能complete。MaxFilesPerVersion继续同时限制草稿与版本清单；批量文件操作仍为1–1000项。请求体上限可能先于文件条数上限触发，两者都要满足；这些限制不构成租户存储容量预留。

大JSON显式保存为普通文件，例如application/json的agent-config.json。payload只保存调用方所需的小型描述信息和入口逻辑路径；入口字段由业务约定，不增加artifact固定schema。客户端负责JSON解析、结构验证和按需下载；storage仍只确认存在及大小，不因Content-Type声明验证JSON语法。草稿/版本通过普通files清单持有该文件，引用、复用、并发与回收完全沿用文件契约。

修改大JSON内容时上传新FileID，再以expectedRevision替换草稿清单；已发布版本继续引用原文件。服务不自动把超限payload转存文件，也不在GET时隐式下载并还原payload。内联payload保留数据库快照语义，外部文件字节仍由客户端负责。文件化不减少每次全量修改的上传字节量；客户端可合并连续编辑后保存，未成功替换清单前继续保留原文件引用。

没有文件的版本不创建 StorageFile 或 VersionFile，响应 files 始终为 []。

~~~json
{
  "requestKey": "request-001",
  "payload": {
    "model": "example",
    "temperature": 0.7,
    "systemPrompt": "你是一个客服助手"
  }
}
~~~

~~~json
{
  "id": "v01",
  "artifactID": "a01",
  "version": "1.0.0",
  "versionUri": "artifact://t01/artifacts/prompt/a01/versions/v01",
  "payload": {
    "model": "example",
    "temperature": 0.7,
    "systemPrompt": "你是一个客服助手"
  },
  "files": []
}
~~~

### 文件内容

调用方先向 storage 登记文件、申请授权、直传并确认 ready，再向 artifact 发布 `files: [{path, fileID}]`。逻辑路径在发布时由 artifact 检查并冻结，不在 storage 登记。可以同时提交 payload；只有文件时 payload 默认为 {}。服务端不接收或代理上传、下载字节。

~~~json
{
  "requestKey": "request-002",
  "bump": "minor",
  "payload": {"framework": "pytorch"},
  "files": [{"path": "weights/model.bin", "fileID": "f01"}],
  "aliases": [
    {"name": "production", "expectedRevision": 3}
  ]
}
~~~

文件响应包含逻辑路径、稳定引用、实际确认大小及可选declaredChecksum；摘要仅表示客户端声明，缺失返回null，不承诺字节相符。读取凭证通过统一 storage grants 接口申请，临时 STS 不保存在版本或幂等结果中。

### 管理信息

Annotations 保存描述及标签等管理信息，与 Payload 分开。Payload 与文件清单在发布后不可修改。修改 Annotations 或 Protected 必须检查 Version.Revision，并在 Artifact 锁内完成；保护变化会影响保留计算。

Protected 是集合管理者统一维护的版本保护标志，不是每个消费者分别持有的保护记录。服务不登记消费者身份、不累计保护数量；管理者解除保护前负责确认各调用方已不再需要保留该版本。Revision 防止并发修改覆盖，不证明下游依赖已解除。

发布请求可传 protected=true，省略或 false 表示不保护。请求 true 时在发布事务中将新 Version.Protected 设为 true，不需要先发布再设置保护，也不额外检查保护管理权限。任一业务条件检查失败，整个发布失败；新版本的保护状态与快照、编号和 alias 更新一同提交。

## 两种版本的核心流程

[打开交互 HTML：逐步查看带文件版本的接口、请求与返回](diagrams/file-version-request-flow.html)。

无文件：客户端 → artifact.CreateVersion → 同一数据库事务提交版本、编号、alias、保留决定。

带文件：

~~~mermaid
sequenceDiagram
    participant C as 客户端
    participant A as Artifact
    participant S as Storage模块
    participant O as S3存储
    C->>S: 登记文件
    S-->>C: FileID
    C->>S: POST storage/grants（scope.preset=upload）
    S-->>C: 一份STS与全部文件位置
    C->>O: 直接上传（含客户端分片）
    C->>S: complete
    S->>O: Stat存在与大小
    S-->>C: ready
    Note over C,S: 此时即可申请download预览，无需先保存草稿或发布
    C->>A: 发布JSON及files(path,fileID)
    Note over A: 持久记录 attempt 与新增引用计划
    A->>S: BindReferences（storage独立事务）
    S-->>A: 引用已持久保留
    Note over A: 本地事务重新校验并提交版本、清单、成功回执及释放任务
    A-->>C: Version
    A->>S: 后台 ReleaseReferences（旧引用，幂等重试）
    C->>S: POST storage/grants（fileIDs及download）
    Note over S: 检查可信归属及ready未过期或bound
    S-->>C: 限权STS与文件位置
    C->>O: 直接读取
~~~

首期 artifact 与 storage 可以同进程、同库部署，但分别持有自己的事务，不跨域传递 tx。独立部署沿用同一引用交接协议、成功条件与故障恢复，不保留共享事务快捷路径。外部交接期间不持业务数据库锁；失败允许暂时多保留引用，不能让成功业务缺少持有关系。此前带 UploadSession 的 HTML/SVG 会话流程已过时；当前四流程见[交互说明](diagrams/file-version-request-flow.html)。

## GORM 数据模型

所有默认值由业务构造逻辑显式填充，布尔字段不依赖数据库默认 true。字段、主键、索引及外键关联统一在 GORM 模型中声明，通过 AutoMigrate 建表和补齐结构。下方集中展示模型，实施时按每张表拆到对应 table 文件，具体目录见代码结构设计。使用 datatypes.JSON 按 MySQL JSON 和 PostgreSQL JSONB 保存 JSON 内容。[GORM 数据类型说明](https://gorm.io/docs/data_types.html)

~~~go
package table

import (
	"time"

	"gorm.io/datatypes"
)

type Artifact struct {
	ID string `gorm:"size:36;primaryKey"`

	TenantID string `gorm:"size:64;not null;uniqueIndex:uk_artifact_name,priority:1"`
	// 租户级为 NULL，项目级为租户服务的项目 ID。
	ProjectID *string `gorm:"size:64;index"`
	// 2026-10-18 新增：资源归属用户，可空。数据库列保持 user_id，Go 字段为 OwnerUserID。
	OwnerUserID *string `gorm:"column:user_id;size:64;index:idx_artifacts_user_id"`
	// 共享为 tenant 或 project:<ProjectID>；个人为 user:<项目UTF-8字节数>:<项目ID或空>:<归属用户ID>。
	ScopeKey string `gorm:"size:160;not null;uniqueIndex:uk_artifact_name,priority:2"`
	// 调用方传入的稳定业务类型，如 skill、model；创建后固定。
	Type string `gorm:"size:64;not null;uniqueIndex:uk_artifact_name,priority:3"`
	// 规范化的小写集合名称，在同一租户、作用域及类型内唯一。
	Name string `gorm:"size:128;not null;uniqueIndex:uk_artifact_name,priority:4"`

	VersionScheme  string `gorm:"size:16;not null"`
	InitialVersion string `gorm:"size:64;not null"`
	LastSequence   int64  `gorm:"not null"`
	LastMajor      int64  `gorm:"not null"`
	LastMinor      int64  `gorm:"not null"`
	LastPatch      int64  `gorm:"not null"`

	// active | archived。archived 后拒绝新发布，保留编号身份。
	State           string `gorm:"size:16;not null"`
	LastPublishedAt *time.Time
	NextRetentionAt *time.Time `gorm:"index"`

	CreatedBy string `gorm:"size:64;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Version struct {
	ID string `gorm:"size:36;primaryKey;uniqueIndex:uk_version_owner,priority:2"`

	ArtifactID string `gorm:"size:36;not null;uniqueIndex:uk_version_owner,priority:1;uniqueIndex:uk_version_seq,priority:1;uniqueIndex:uk_version_number,priority:1;index:idx_version_state,priority:1"`
	Sequence   int64  `gorm:"not null;uniqueIndex:uk_version_seq,priority:2"`
	Number     string `gorm:"size:64;not null;uniqueIndex:uk_version_number,priority:2"`

	Major *int64
	Minor *int64
	Patch *int64

	Payload     []byte `gorm:"not null"`
	Annotations []byte `gorm:"not null"`
	Revision    int64          `gorm:"not null"`
	Protected   bool           `gorm:"not null"`
	// ready | deleting | deleted。
	State string `gorm:"size:16;not null;index:idx_version_state,priority:2"`

	CreatedBy   string `gorm:"size:64;not null"`
	PublishedAt time.Time
	DeletedAt   *time.Time

	Artifact *Artifact `gorm:"foreignKey:ArtifactID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}

type Alias struct {
	ArtifactID string `gorm:"size:36;primaryKey"`
	Name       string `gorm:"size:64;primaryKey"`
	// 逻辑删除时清空目标，保留修订号。
	VersionID *string `gorm:"size:36;index"`
	Revision  int64   `gorm:"not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time

	TargetVersion *Version `gorm:"foreignKey:ArtifactID,VersionID;references:ArtifactID,ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}

type RetentionPolicy struct {
	ArtifactID string `gorm:"size:36;primaryKey"`
	Enabled    bool   `gorm:"not null"`

	MaxVersions   *int64
	MaxAgeSeconds *int64
	MinVersions   int64 `gorm:"not null"`

	// reject | deleteOldest。
	OverflowAction string `gorm:"size:16;not null"`
	ProtectAliases bool   `gorm:"not null"`
	Revision       int64  `gorm:"not null"`

	LastEvaluatedAt *time.Time
	LastResult      datatypes.JSON `gorm:"not null"`
	UpdatedAt       time.Time

	Artifact *Artifact `gorm:"foreignKey:ArtifactID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}

// artifact 自有模型；不与 storage 表建立跨域外键。
type VersionFile struct {
    VersionID string `gorm:"size:36;primaryKey;uniqueIndex:uk_version_file_id,priority:1"`
    LogicalPath string `gorm:"size:512;primaryKey"`
    FileID string `gorm:"size:36;not null;uniqueIndex:uk_version_file_id,priority:2"`
    ReferenceID string `gorm:"size:36;not null;uniqueIndex"`
    Size int64 `gorm:"not null"`
    ContentType string `gorm:"size:128;not null"`
    DeclaredChecksumAlgorithm *string `gorm:"size:32"`
    DeclaredChecksumType *string `gorm:"size:16"`
    DeclaredChecksumValue *string `gorm:"size:256"`
    Version *Version `gorm:"foreignKey:VersionID;references:ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}

type CreateRequest struct {
	ArtifactID string `gorm:"size:36;primaryKey"`
	ActorID    string `gorm:"size:64;primaryKey"`
	RequestKey string `gorm:"size:64;primaryKey"`

	RequestHash string `gorm:"size:64;not null"`
	VersionID   string `gorm:"size:36;not null"`
	CreatedAt   time.Time

	Version *Version `gorm:"foreignKey:ArtifactID,VersionID;references:ArtifactID,ID;constraint:OnUpdate:RESTRICT,OnDelete:RESTRICT"`
}

type BackgroundTask struct {
	ID       string `gorm:"size:36;primaryKey"`
	Kind     string `gorm:"size:32;not null;uniqueIndex:uk_task_target,priority:1"`
	// 引用任务使用 AttemptID 或独立删除交接 ID，不使用可复用请求键。
	TargetID string `gorm:"size:36;not null;uniqueIndex:uk_task_target,priority:2"`
	// 不可变完整释放计划：ReferenceID/FileID、精确作用域及每项 NotBefore。
	Payload datatypes.JSON `gorm:"not null"`

	// pending | running | done | failed。
	State      string     `gorm:"size:16;not null;index:idx_task_due,priority:1;index:idx_task_lease,priority:1"`
	NextRunAt  time.Time  `gorm:"index:idx_task_due,priority:2"`
	LeaseUntil *time.Time `gorm:"index:idx_task_lease,priority:2"`
	LeaseToken string     `gorm:"size:36;not null"`

	Attempts  int32  `gorm:"not null"`
	LastError string `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
~~~

### storage 自有模型与表边界

artifact 的十一张表是 Artifact、Version、Draft、DraftFile、DraftCreateRequest、Alias、VersionFile、RetentionPolicy、CreateRequest、ReferenceChange、BackgroundTask。storage 独立 table 包持有三张表，共十四张。

ReferenceChange 唯一键为 (ArtifactID,ActorID,OperationKind,RequestKey)，保存可信作用域、固定 RequestHash、当前 AttemptID、preparing/committed/aborted、执行 Deadline、完整新增 ReferenceID/FileID 及原始 ExpiresAt 计划、结果身份和可选 AppliedRevision。ArtifactID 是域内外键，结果 DraftID 不设硬删级联。指纹包含 DraftID、expectedRevision 等完整输入。旧 attempt 的不可变补偿载荷同事务交给 BackgroundTask 后，才可切换到新 attempt。终态可清理已可靠交接的准备明细，保留去重和决策所需小型记录；不保存大 JSON 用于后台重放。

引用计划中的可信作用域保存为 `storage.Scope` 类型的 JSON 载荷，包含 TenantID、ProjectID 和可选 OwnerUserID。内部 JSON 键名使用 `userID`（而非公开响应的 `ownerUserID`），以便旧版本个人计划在后台释放时仍能正确识别归属；公开 HTTP/SDK 响应使用 `ownerUserID`，数据库列保持 `user_id`。这一内部与公开命名差异仅影响持久计划载荷，不影响请求选择器或响应字段。

DraftFile 同样保存内部唯一 ReferenceID；公开清单仍只使用 FileID/path 及确认元数据，客户端不提交 ReferenceID。



| 表 | 必须保存的字段与约束 |
| --- | --- |
| storage_files | ID主键；TenantID、ProjectID、OwnerUserID（可空，数据库列 user_id）、ScopeKey、CreatedBy；RequestKey、RequestHash；唯一(TenantID,ScopeKey,CreatedBy,RequestKey)；BackendID/Bucket/ObjectKey位置唯一；ExpectedSize、确认后的Size、ContentType、三项可空声明摘要；State；ExpiresAt、LastGrantExpiresAt；CreatedAt、UpdatedAt、DeletedAt、NextSweepAt |
| storage_references | (ConsumerID,ReferenceID)联合主键；不可变FileID、TenantID/ScopeKey；FileID外键到storage_files；CreatedAt、ReleasedAt（NULL表示有效）、ReleaseRequiresDeletion；按FileID查有效引用的索引；不存在业务owner字段或跨域外键；先Release可创建终态墓碑 |
| storage_tasks | ID、Kind、TargetID唯一目标；State、NextRunAt、LeaseUntil、LeaseToken、Attempts、LastError、CreatedAt、UpdatedAt；独立于artifact后台任务表，由storage拥有领取与执行 |

State 为 uploading、ready、bound、deleting、deleted。初始 ExpectedSize 固定，确认前 Size 为 NULL；成功确认后 Size 等于 ExpectedSize。StorageReference 保存每次持有关系及终态墓碑；bound 当且仅当存在有效引用，deleting/deleted 必须无有效引用。引用写入和计数判断都在对应文件行锁下进行。storage 仅认识服务认证产生的 ConsumerID、ReferenceID、FileID 及可信作用域（含 OwnerUserID），不知道版本、草稿、alias 或保留规则。两域分别声明模型清单和初始化入口，可以使用同库或独立库，事务边界始终独立。

### 唯一性与外键

租户级 ProjectID 为 NULL，不能依赖 (TenantID, ProjectID, Type, Name) 唯一索引约束重名。共享 ScopeKey 为 `tenant` 或 `project:<ProjectID>`；两种个人范围统一为 `user:<项目ID的UTF-8字节数>:<项目ID或空>:<归属用户ID>`，无项目时为 `user:0::<归属用户ID>`。项目长度前缀避免标识中包含冒号时发生碰撞。ScopeKey 最长 160 字符。唯一键采用 (TenantID, ScopeKey, Type, Name)。ScopeKey 是内部约束字段，不增加新的领域作用域。

通过模型关联的 foreignKey、references 和 constraint 标签声明以下外键，AutoMigrate 负责创建；仅声明 ArtifactID、VersionID 等标量字段不会自动生成外键：

- Version、Draft、RetentionPolicy 的 ArtifactID 引用 Artifact.ID。
- Alias 的 (ArtifactID, VersionID) 引用 Version 的 (ArtifactID, ID)。
- DraftFile.DraftID 引用 Draft.ID；VersionFile.VersionID 引用 Version.ID；FileID 不建立到 storage 的跨域外键，预先持久绑定及引用交接决策保证引用有效。
- CreateRequest 的 (ArtifactID, VersionID) 引用 Version 的 (ArtifactID, ID)。
- DraftCreateRequest.ArtifactID引用Artifact.ID，DraftID不建外键，原草稿硬删后仍保存幂等结果身份。

Alias 的可空 VersionID 表示已解除引用。后台任务目标有多个类型，由业务层校验，不建立多态外键。跨服务的 TenantID、ProjectID 来自接入方验证的 context，不建立跨服务数据库外键。

名称与 alias 采用规范化的小写 ASCII 标识。文件路径保留大小写；MySQL 建表配置为路径与对象 key 指定区分大小写且不忽略尾随空格的排序规则，PostgreSQL 使用确定性比较规则。需要做实际双数据库唯一性测试。

### 默认值与可变字段

- 新 Artifact：active，LastSequence 为 0，InitialVersion 为 1 或 1.0.0。
- 新 Version：ready，Revision 为 1；Protected 默认 false，发布请求可指定 true；Payload、Annotations 至少为 {}。
- Alias 初次创建 Revision 为 1；删除后再次绑定继续递增。
- 未配置或 disabled 的策略不执行自动清理；新建策略 MinVersions 默认 1、ProtectAliases 默认 true。
- maxVersions 与 maxAgeSeconds 为 NULL 表示未限制，为正数表示启用。
- 配置策略的 Revision 只随配置修改增长；LastResult 是执行结果，不修改配置修订号。

模型不嵌入 gorm.Model，也不使用 GORM 的默认软删除行为。所有读取显式判断业务 State，防止后台删除流程被隐式 scope 干扰。

## 发布事务与并发控制

### 锁定范围

同一个 Artifact 的发布、alias 更新、保护更新、保留策略配置、逻辑删除和保留执行共用 Artifact 行锁。不同集合独立执行。

使用 READ COMMITTED 隔离级别与 GORM clause.Locking 的 UPDATE 锁。锁按稳定 Artifact.ID 查询，创建集合与发布版本是两个明确操作，因此首版也有可锁记录。编号唯一索引提供最终约束。

数据库支持依据：[PostgreSQL 锁定查询](https://www.postgresql.org/docs/16/sql-select.html)、[PostgreSQL 隔离级别](https://www.postgresql.org/docs/16/transaction-iso.html)、[MySQL InnoDB 锁定读取](https://dev.mysql.com/doc/refman/8.4/en/innodb-locking-reads.html)、[GORM 锁接口](https://gorm.io/docs/advanced_query.html#Locking)。

### 创建版本的交接与本地事务步骤

1. 检查可信身份、精确 scope、输入结构及整包大小，规范化创建来源和请求指纹。直接 files 省略或 [] 表示无文件，null 拒绝；每项只有 path/fileID，拒绝重复路径及 FileID。查询成功 CreateRequest；命中直接返回原身份或410，不准备新引用。
2. 每个未成功的新 attempt 都检查 active、未归档、来源可见性与指定修订、清单数量及 JSON 大小。通过 artifact 短事务读取一致业务快照，再从 storage 取得新增文件的确认元数据和可信原始 ExpiresAt；查询不构成绑定保证。
3. 短事务按 Artifact → 可选 Draft/来源 Version → ReferenceChange 串行决策，锁内重查成功回执和指纹。有效 preparing 返回 RequestInProgress；过期 attempt 原子 aborted 并写不可变补偿任务后才可换新 attempt。新 AttemptID、全部新 ReferenceID/FileID 及原始期限先持久记录，不提前推进编号或移动 alias。
4. 不持 artifact 锁调用 BindReferences，可按 storage 批大小拆分；每批在 storage 自己的事务内全成或全败，全部计划绑定确认后才进入业务提交。客户端不能提交大小或物理位置作为清单事实。没有新增引用的纯 JSON 发布省略准备查询和 Bind，仍遵守本地成功去重。
5. 最终 artifact 本地事务按上述锁序重查当前 AttemptID、preparing、执行判定时未过截止时间、归属 active 与未归档。草稿发布重查指定 Revision；来源复制重查可见性与相关 Revision，不能静默替换准备快照。校验 alias 名称及 Revision，计算编号与 retention.Plan，新版本不参与本次淘汰。
6. 写 ready Version、VersionFile（含独立 ReferenceID）、alias、编号与成功 CreateRequest、ReferenceChange.committed；对可淘汰旧版本调用 MarkDeleting，修改业务可见性并写含完整旧引用的不可变释放任务。以上全部在 artifact 本地事务提交；纯 JSON 旧版本可直接完成删除。
7. 确认提交后返回成功；后台按持久任务执行 ReleaseReferences，再用 GetReferenceStatuses 等待需要的删除回执。业务失败先持久 aborted，再为全部计划新引用补偿；不以 Bind 响应是否收到决定补偿范围。

本地事务失败只回滚业务快照、编号、alias、保留决定与本次任务写入；先前独立 Bind 走补偿，不声称跨库回滚。事务中不访问 storage/S3/STS；最终提交与 abort 竞争同一 ReferenceChange 决策。storage 自己按 FileID 升序加锁，与 GC 共用文件锁；没有 Artifact → StorageFile 跨域锁链。完整超时、补偿窗口和乱序规则见后文引用交接契约。

### 幂等与重试

requestKey 对创建版本必填。 已确认指纹忽略JSON空白及对象键顺序，使用json.Number保留数值词法形式；1、1.0、1e0不同。同名alias拒绝，唯一alias列表按名称排序，payload数组顺序保留。对象重复键与实际请求统一按标准库解码以后值生效，不额外引入解析器。指纹由校验并应用默认值后的请求做确定性编码并计算摘要，包括 payload、bump、files 清单、初始 protected 和 alias 条件，以及其他影响创建结果的请求字段。files 按 path 排序后编码，省略与 [] 等价，改变文件或逻辑路径即改变指纹；草稿来源指纹保存 draftID/expectedDraftRevision，不重新读取当前内容。protected 省略与 false 规范化为同一值。相同键、不同参数返回 409；重试核对初始请求指纹，不把版本后来被管理者修改的 Protected 当作请求参数变化，也不恢复其初始保护状态。

提交成功但响应丢失时，客户端使用相同 requestKey 重试，即使落到其他副本也得到同一版本。版本已进入 deleting 或 deleted 时，重试返回 410 并携带原版本身份，不返回原 payload 或文件清单，不恢复内容或分配新编号；这一判断按逻辑可见性执行，不等待物理回收完成。

集合归档阻止新发布、首次创建和编辑草稿；保留历史读取、条件删除草稿，以及身份与作用域校验后的已成功创建/发布/保存结果确认。因此，已成功请求的去重查询先于 active 检查；归档后的相同请求仍返回原版本身份，若版本已删除则按删除结果返回。重试不再次绑定文件、移动 alias、推进计数器或执行保留淘汰。

数据库死锁与可重试事务错误在 context 截止时间内做有上限重试。仅重试明确已回滚的本地事务，重入时检查 CreateRequest 及当前 attempt；未知提交结果先查询主库决策，不盲目重放或补偿。业务参数错误、身份或作用域错误和保留阻塞不自动重试。

已有 server-go HTTP 幂等缓存只在单进程内有效，本服务的跨副本保证来自 CreateRequest。身份与作用域检查始终先于去重结果返回。

## alias 行为

alias 在 Artifact 内唯一。第一版名称为小写 ASCII 标识，保留 latest 作为系统查询名称。

- latest 查询 Sequence 最大的 ready 版本，不建立用户可移动 alias 记录。
- 普通 alias 可以指向任何 ready 版本，用于发布、回滚或固定使用。
- 首次创建从未存在过的普通 alias 使用 expectedRevision=0。
- 修改或删除必须提供当前 expectedRevision；不匹配返回 409。
- 删除保留 alias 墓碑和 Revision，避免删除重建后旧请求误覆盖。
- 重新绑定已删除 alias 必须提交墓碑当前 Revision，成功后继续递增；expectedRevision=0 不能覆盖已有墓碑。
- 删除目标版本前必须解除普通 alias 引用。显式 Protected 始终阻止删除。
- alias 解析读取主库；不能返回缓存中的旧指向作为强一致结果。

普通 GET alias 返回引用自身的 Revision 及实际目标身份。当前作用域内的调用方可通过 GET /v1/artifacts/{artifactID}/aliases/{name}?includeDeleted=true 读取墓碑：返回 Revision、DeletedAt 和空 VersionID，用于条件重新绑定；名称从未存在时仍返回 404。普通 GET 与 ResolveURI 不把墓碑解析成版本，已删除引用返回不存在。includeDeleted 查询同样执行身份和归属检查，不改变目标版本的可见性。

同一用户可进入多个租户不会产生默认 alias 搜索范围，解析始终限定 URI 指定的集合。

## storage 文件与 artifact 清单的边界

storage 负责登记、授权、确认、绑定状态与物理回收；artifact 负责版本、逻辑文件清单、可见性与保留决定。两者暂时同进程部署，文件 API 使用独立 `/v1/storage` 前缀和 storagev1 SDK。不存在 UploadSession、seal、download-session，也没有文件字节代理。

### 文件登记、授权和确认

登记与确认采用逐项独立的批量接口：POST /v1/storage/files 的请求体直接是登记项数组 `[{requestKey,size,contentType?,declaredChecksum?}]`，POST /v1/storage/files/complete 的请求体直接是 FileID 字符串数组 `["f01","f02"]`。两者不包 files/fileIDs 对象。单文件使用一个元素，每批1–1000项。空数组、重复requestKey或FileID、超过上限及无效请求结构整体400；请求身份无效整体401/403。合法请求返回200，响应体直接是逐项结果数组 `[{...}]`，不包 files、items 或 data，保持输入顺序及输入项数；登记项带requestKey，确认项带fileID。成功项包含文件字段且error=null；失败项只有关联标识与error{code,message,status}，status是HTTP状态而非框架statusCode。参数、状态、归属和后端失败逐项返回，跨作用域项404，不回滚其他成功项；SDK整体error不表示每项成功。顶层 null、非数组、旧包装对象、登记数组中的非对象/null元素、确认数组中的非字符串/null元素均为整请求400，逐项操作前完成结构检查；缺失请求体或无效JSON同样拒绝。有效结构中的登记项字段参数错误仍按既定逐项错误返回。整请求400/401/403/413等失败返回标准错误对象，不伪造成结果数组。

每个登记项独立按requestKey幂等；失败只重试失败项，响应丢失可重试整批。统一授权 POST /v1/storage/grants 单独处理：同一批fileIDs共用一组S3 Actions及一份STS，整体成功或失败，不使用上述逐项结果结构。文件夹由客户端展开，逻辑相对路径只进入草稿/版本清单，授权始终使用每个FileID登记的精确key。超大集合可以拆批登记、授权、确认；最终草稿保存或发布仍受清单大小限制，业务快照在 artifact 本地全量原子提交，引用按持久协议交接。分片由客户端S3 SDK执行。

以下为每项处理规则：

1. `RegisterFile` 接收必填 requestKey、size（含零）、可选 contentType 和 declaredChecksum。可信 scope 写入 StorageFile，创建新 FileID 与独占 key，状态 uploading，期限由 storage 配置。新登记要求归属active。首次生成记录；同(TenantID,ScopeKey,ActorID,requestKey)且参数相同返回原记录，不延期、不复活；不同参数为该项409。比较默认 contentType 和可选声明摘要，省略/null声明等价。终态或归属删除后的成功登记重试，凭可信历史scope仍返回原记录及状态，不新增上传权限。
2. `CreateGrant` 按下节统一契约申请或续发权限。登记不附带凭证；需要上传时显式申请upload预设或所需S3 Actions，下载复用同一接口。
3. 客户端直接上传；multipart 的 UploadID、part、重试、合并及主动 Abort 全由客户端 S3 SDK 管理。
4. `CompleteFile` 先查文件，事务外 Stat 存在与实际大小；再在短事务内锁同一文件，检查仍 uploading 且未过期，记录 Size 并转 ready。失败不直接 Delete、不转终态，保留 uploading 供有效期内修正后重试。ready/bound 重复确认返回已确认记录，不重新检查字节或更改引用；终态不能由确认复活。GetFile 可查询响应丢失后的状态。
5. ready 后停止签发新上传凭证，可在 ExpiresAt 前保存进草稿或发布绑定；ready 仍未绑定而到期，或未绑定文件被显式删除，原子转 deleting 并创建 storage 回收任务。公开 DeleteFile 对 bound 返回409，不接受 owner 参数，不可绕过版本保护；deleting 重复删除202，deleted重复删除200。

客户端负责真实内容；确认只核对存在与大小。DeclaredChecksum 三项整体 NULL 或整体有效，仅表达客户端声明；FULL_OBJECT/COMPOSITE 不表示服务已验证，不把 ETag 或对象自定义元数据当作实际摘要，不做全局去重。发布后 JSON/清单冻结，外部字节可被客户端改写；服务不复制封存、不撤回已签发上传凭证、不持续监测或恢复 bound 对象。

### 统一 Grant：S3 权限预设与自定义 Actions

公开入口为 POST /v1/storage/grants，SDK 方法为 storagev1.Client.CreateGrant。请求只包含fileIDs、scope及可选ttlSeconds，不接收ArtifactID、VersionID、DraftID、owner或业务读取来源。scope 表示本次申请的S3权限集合，与请求身份的TenantID/ProjectID作用域是两个概念。

- fileIDs 必填，1–1000个唯一FileID；同批文件使用相同Actions，一份STS覆盖全部精确key。不同文件需要不同权限时分别申请，不建立资源与动作的交叉配对模型。
- scope 必须且只能是 {preset:"upload"}、{preset:"download"} 或 {actions:["s3:..."]}。preset与actions按字段存在互斥，显式null、空对象、未知字段、空actions、重复Action、未知preset、通配符及未支持Action均400 InvalidGrantScope。自定义Actions使用S3原名，不另设object.write等翻译层。
- upload预设展开为s3:PutObject、s3:ListMultipartUploadParts、s3:AbortMultipartUpload；download展开为s3:GetObject。首期自定义支持这四个Actions的非空子集。预设是服务端固定契约，不接受用户重定义；自定义申请仍受相同的业务条件与服务端允许范围限制，不接收原始IAM Policy、任意bucket/key或owner。
- PutObject覆盖普通PUT和multipart创建、分片上传、完成；ListMultipartUploadParts用于已知UploadID恢复；AbortMultipartUpload只中止未完成分片。GetObject覆盖GET、Range和HEAD，不能伪造“只HEAD不下载”的权限。客户端保存UploadID，不依赖ListBucketMultipartUploads列桶恢复。不支持DeleteObject、ListBucket、ACL/标签及bucket/IAM管理操作。
- 所有请求都由storage检查可信 TenantID、ProjectID、ResourceUserID 与每个 StorageFile 的 TenantID、ProjectID、OwnerUserID 精确一致；跨作用域或不存在文件404，已知FileID本身不是访问凭证。同一完整作用域内不再按文件创建者区分权限，也不检查每个 Artifact 的独立权限。身份与作用域仍由可信接入层验证，不能信任客户端自行声明。
- GetObject允许未过期ready文件，或仍有有效引用的bound文件；不要求已保存草稿或发布版本，不检查业务清单、owner来源或版本可见性。文件可以来自不同版本、草稿或尚未绑定业务，只要处于同一可信作用域和配置后端，且满足状态条件，就能放在同一批中授权。ready和bound也可混批读取。
- 任一上传相关Action仍要求active归属、同精确身份作用域、未过期uploading文件。uploading不签发新读取权限，ready/bound不签发新上传权限，deleting/deleted拒绝任何新授权；已知同作用域文件状态或期限不满足时409。混合GetObject与上传Actions无法同时满足当前状态要求，整体409，不自动拆分或缩小权限。首期仍不接入租户角色权限系统；已识别动作被服务允许规则拒绝时403 GrantScopeDenied。自定义Actions不能绕过相同的归属和状态约束。
- ttlSeconds省略时使用所请求动作对应的配置上限，提供时必须为正整数。实际有效期取申请期限、所有相关配置上限、签发者剩余期限，以及所有上传目标和读取时仍为ready的目标ExpiresAt的最小值。读取bound文件不使用其历史准备ExpiresAt；整批读混合ready/bound时，仍取全部适用上限的最小值。绝对截止时间在锁内判定时固定，不因STS调用延迟延长；返回时已过期则拒绝。签发者最短期限无法满足时拒绝，不能向上取整越界。

全部条件通过才签发，成功HTTP200返回一份GrantResponse：endpoint、region、forcePathStyle、expiresAt、最终展开去重并按字典序排列的actions、credentials{accessKeyID,secretAccessKey,sessionToken}，以及按输入顺序的files[{fileID,bucket,objectKey}]。上传和下载都使用此形状，不返回预签名GET URL。任一文件的身份归属、状态或动作校验失败则整体错误，不返回部分凭证或成功项；后端签发失败也不返回凭证。所有文件必须属于同一配置后端；不同后端请求拒绝。原始资源响应与日志不得包含凭证。

STS Policy的Action精确等于最终actions，Resource仅为登记位置的精确对象ARN集合，例如arn:aws:s3:::tenant-t-<id>/files/<fileID>，包含实际部署固定前缀；不得使用bucket/*或目录通配。1–1000是接口数量上限，不保证后端Policy一定容纳得下。实际编码超过签发者支持范围，含后端返回策略过大，统一400 GrantScopeTooLarge，由调用方拆批；不自动扩大范围、丢弃目标或拆成未声明的多份凭证。会话Policy必须实际限制到这些Actions与Resources，不能忽略Policy签发更宽凭证。

storage.CreateGrant自己拥有授权事务与签发流程，不向artifact暴露准备/签发中间步骤。它在短事务中按StorageFile ID升序锁全部目标，核验可信归属、动作、状态和适用期限，确定精确位置与绝对授权截止时间；不锁Artifact或Draft，也不查询其表。所有条件通过后，为每个上传目标更新LastGrantExpiresAt=max(已有值,本次拟签发截止上限)，提交后在事务外调用Adapter.CreateGrant签发STS。失败保留上限，不缩短它。签发后对整批上传目标重新检查身份归属有效性、状态及期限；任一目标已ready/bound/终态、过期或归属失效，整份凭证不返回，已产生凭证仍纳入原预留等待。读取授权以锁内文件可读状态为判定点，不在签发后回查业务或重复检查文件状态；绝对期限仍不得延长或在已过期时返回。没有持久Grant会话；续发仍调用CreateGrant并重新验证，可能产生与旧凭证并存的新凭证。

精确key权限不等于一次写入，也不隔离单个UploadID；有效期内可重复PUT或为同key建立多个分片上传。它不保证防覆盖或大小正确，仍由complete核验。ready/bound停止签发新上传权限，已签发凭证自然到期；只读权限不更新LastGrantExpiresAt，也不延缓GC。

S3动作依据[AWS权限对应关系](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html)与[multipart权限说明](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpuoverview.html)；会话Policy受签发者权限交集与策略大小限制，见[STS AssumeRole](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html)。上述为目标契约，实际JuiceFS普通IAM STS的多key精确限制、自定义Actions、读取/上传及有效期尚需验收，失败不得降级宽权限或把历史探针当作通过。

### HTTP步骤与事务边界的取舍

单个HTTP请求也可以先调用外部存储、再开启短数据库事务；合并接口在技术上可行。是否合并应看独立使用与失败恢复成本，不能把HTTP接口数量当作数据库原子性的要求。

原子性要求落在各域本地结果：草稿 JSON/清单/Revision/成功回执/释放任务一起提交，版本编号/快照/alias/保留/成功回执/释放任务一起提交。新增引用提前在 storage 独立事务持久绑定；业务失败可靠补偿，不跨库回滚。上传、STS/Stat 和引用 RPC 均不加入 artifact 事务。文件登记和确认逐项独立，grant 整批授权，业务快照全量原子，各自失败边界明确。

登记→授权→直传→确认→保存草稿→发布是可恢复的分步协议，不要求每一步由用户手工操作。登记确定FileID与声明；独立grant服务上传、下载、自定义权限和续发；确认建立ready事实；保存确定可编辑修订；发布赋予版本编号并应用alias/retention。登记不合并首次授权，避免把文件创建绑定到一次短期凭证签发。SDK和Web端可编排这些步骤，API保留独立重试边界；本版统一为27项HTTP操作，不新增组合发布入口。

确认、草稿保存和发布可独立失败、重试和使用；将其合成一个横跨S3与DB的大事务并不能保证原子。当前保留这些边界。已保存且本地无改动的草稿可直接以已知Revision发布；有待保存内容则先完成确认与条件保存。不是为了发布而无条件新增一次相同内容的PUT。

### 绑定与释放：首期即独立事务的持久交接

完整协议见[引用交接设计](storage-reference-handoff-design-20261015.md)，为首期实施要求。首期内部传输统一为受认证HTTP/JSON，combined也使用回环HTTP，具体消息见[内部线协议](storage-internal-http-contract-20261015.md)。storage 提供可信服务操作 BindReferences、ReleaseReferences、GetReferenceStatuses，不进入普通用户 SDK，不接收调用方 tx。ConsumerID 由服务认证产生，可信精确租户/项目随请求传递；普通客户端不能伪造 consumer 或持有关系。

BindReferences 在自己的事务内按 FileID 升序锁文件，检查 ready 未过期或仍有有效引用的 bound，建立 (ConsumerID,ReferenceID) 对 FileID 的不可变持有关系并返回确认元数据。同批原子，大清单分批；多批失败时业务不提交，全部计划引用都补偿。相同 active 引用同参重试幂等，异参拒绝。released 是终态，永不恢复；移除再加入、失败重试、复制和发布都用新的 ReferenceID。草稿未变文件或只改路径沿用原引用。

ReleaseReferences 只结束指定 consumer 的引用，不查业务 owner。不存在的引用也写 released 墓碑，拒绝迟到 Bind；未曾 active 的引用释放不触发文件 deleting。实际 active 引用释放后还有其他引用则保持 bound，该释放结果固定完成；最后引用释放则转 deleting 并写 storage 回收任务，GetReferenceStatuses 等待一次物理删除回执。FileID/作用域不匹配仍拒绝。首期引用墓碑不自动清理、不设 TTL，即使 FileID 已 deleted 也保留 GetReferenceStatuses 的稳定释放回执。未来清理须先设计等价终态回执，本期不实现。

artifact 在任何 Bind 前持久保存 ReferenceChange 及全部计划引用。最终本地提交检查当前 AttemptID/preparing/执行截止时间，并原子提交业务、成功回执、committed 和旧引用释放任务。中止则原子 aborted 和全部计划引用的不可变补偿任务；任务以 Kind+AttemptID 去重，删除用独立交接 ID，不能用可复用 requestKey 覆盖旧计划。换新 attempt 前必须交出旧计划。worker 只中止到期准备并发送释放，不接管业务提交；不需要存大 JSON 重放。

已 aborted 的每项补偿 NotBefore 为 max(中止时刻,原准备 ExpiresAt)，包括未知、未发送或查询 absent 的引用；不能以一次查询提前释放，防止迟到 Bind 抢先提交。可信期限在 Bind 前查询并持久保存，不用客户端值、不重置准备 TTL。原期限已过可立即补偿；任务失败可以延长实际保留。用户在原窗口内可用新 attempt/ReferenceID 重试或另存，已成功的引用没有自动过期。

最终提交连接中断时查 artifact 主库决策；无法确认则保留引用并返回 OperationOutcomeUnknown，不因超时或缓存认定失败。有效 preparing 返回 RequestInProgress，确定业务失败必须先持久 aborted。旧进程不能越过新 AttemptID、终态或截止时间提交；commit 与 abort 只能一个成功。同键同参在 aborted 后可开始新 attempt；成功最多一次。

删除和移除清单只在 artifact 本地事务记录不可变释放义务，后台可靠交付；任务重试不读取已删除业务清单。草稿 DELETE 204 不等待 storage；带文件版本仍等待各引用的释放完成回执后收尾。投递前或其他引用仍有效时文件保持 bound，同 scope 已知 FileID 仍可读，业务删除不等于立即撤销文件授权。

bound 不按准备 TTL 回收。GC、BindReferences、ReleaseReferences 共用文件行锁，deleting/deleted 不复活。新版本引用先持久生效，再提交 retention 淘汰和旧引用释放义务，不产生零引用空档。无自动按哈希去重、跨作用域复用或物理复制；外部对象改写可能影响所有引用者，仍由客户端负责。

### 文件读取权限与业务清单可见性

CreateGrant完整归storage模块，由storage/controller绑定请求，storage/grant负责文件授权事务和STS签发。artifact只负责版本/草稿的内容与清单是否可见，以及保存、发布或删除时增减文件引用。客户端通常先取得业务清单，再按FileID向storage申请读取；已经持有合法FileID的客户端也可直接申请。两种路径都执行相同的可信精确作用域与文件状态检查，不新增业务授权票据、artifact回调或跨域grant编排。

引用是保留文件的依据，不是读取ACL。删除版本A会隐藏A的JSON及清单并释放A的引用；如果版本B或某个草稿仍引用F，F保持bound，同作用域中已知F的调用者仍可申请新读取授权。删除某一业务来源不承诺阻止通过该来源得知FileID的调用者再次读取。草稿移除文件、替换附件或删除草稿遵循同一规则；FileID不可访问其他租户/项目/用户范围，OwnerUserID 标识资源归属但不形成创建者独享权限，实际访问仍由 IdentityResolver 授权。将来增加Artifact私有权限必须另行设计文件权限关联，不能沿用本期作用域内共享的假设。

ready且未过期的文件在保存草稿前即可预览；读取不绑定文件、不延长准备TTL，也不新增StorageReference。其读取授权期限受当时ExpiresAt限制，后来绑定不会延长已发凭证。bound不因历史准备ExpiresAt到期而禁读。读取历史资源仍须有效的可信历史作用域，读取本身不新增active归属要求；写授权仍要求active。

grant、BindReferences/ReleaseReferences、ready过期扫描和显式DeleteFile共用StorageFile行锁。非最后引用释放不改变bound和可读性；最后引用释放或未绑定文件回收先将文件转deleting，后来的grant拒绝。若读取授权先在锁内通过，允许之后在事务外完成签发，即使此时引用已释放或对象已删除；截止时间固定，返回时已过期仍拒绝。授权不延缓物理删除，未到期凭证不保证对象仍存在。

客户端用STS直接调用S3 SDK，GetFile只返回状态与元数据，Resolve只返回业务引用及清单，不代理字节。文件授权和读取可以随storage独立部署，不需要理解artifact模型；引用交接从首期沿用上述独立事务协议，物理拆分不改变成功条件或恢复语义；部署仍须验证服务身份、网络失败与两库恢复。

### 存储协议 Adapter

| 操作 | 输入和责任 |
| --- | --- |
| EnsureBucket | 固定 TenantID 派生位置，确认存在/创建；冲突必须验证可访问归属 |
| CreateGrant | 已核准的实际bucket/key集合、S3 Actions与绝对截止时间；编译精确Policy，返回一份限权STS、实际期限及S3连接参数 |
| Stat | 实际位置，返回存在及 Size，不读取文件内容计算摘要 |
| Delete | 实际位置；对象不存在即成功 |

Adapter 仅供 storage 模块使用，隐藏第三方 SDK 类型和协议错误。无 Open/io.ReadCloser 契约。官方 S3 SDK 承担签名和协议，配置区分服务可达的S3/STS地址与客户端可达的S3地址；客户端使用返回的endpoint和region自行签名请求。浏览器上传、分片与直接 GET 所需 CORS 在部署配置中验收。

### 回收与迟到写入

LastGrantExpiresAt只记录包含上传相关Actions的授权，读取授权不延缓物理回收。storage worker 首轮删除等待 LastGrantExpiresAt（若无授权则无需等待），阻止常见的仍有效凭证写回；等待不能保证已经开始的写入绝不迟到。终态文件位置和引用释放墓碑保留，storage 周期扫描这些记录并在发现迟到对象后安排回收，不扫描 bound 文件、不按目录年龄清理、也不需要全 bucket 枚举。回执仅证明一次 Delete 成功/对象不存在，不证明永久不存在；artifact 可据首次全部回执完成删除，之后的迟到写入仍由 storage 清理，不能复活版本或文件。

未完成 multipart 由客户端 Abort 或经实际后端验证的生命周期机制回收；完整对象回收不能被分片生命周期替代。后台任务有界重试，failed 需告警及受控运维重排，周期扫描不得重置 failed 绕过预算；done 任务仅在发现新的迟到对象时形成新的清扫轮次，保留历史失败信息。

### 限制与部署

storage 配置 MaxFileBytes、临时文件期限与上传/读取授权期限；artifact 配置 MaxFilesPerVersion，同时约束草稿与版本清单。无租户字节账本、全局去重或物理容量预留。实际超限上传不能确认 ready，终止后回收。单文件限制不保证对象存储在接收字节时即时拒绝超限。

后端统一 S3，当前本地部署只有 JuiceFS，RustFS 已移除；服务的真实 S3/STS 契约仍需按新流程验收。实际容量还要包含 uploading、ready、bound、deleting 数据。声明摘要探针的历史结果不等于新契约通过。

## 版本保留策略与删除

retention 指版本保留机制：每个物料集合可以配置保留多少版本、版本最长保留多久，以及是否保护 alias 引用的版本。RetentionPolicy 保存规则；Plan 接收规则与版本信息，纯计算候选删除、保留及阻塞原因，不操作数据库或对象存储。所谓“按集合取得策略”就是根据 ArtifactID 读取该集合自己的 RetentionPolicy；HTTP 调用方通过 GetArtifact 响应的 retentionPolicy 字段取得规则和 revision，无须另设取得策略接口。

### 策略语义

| 参数 | 含义 |
| --- | --- |
| maxVersions | ready 版本数量上限，包括受保护版本 |
| maxAgeSeconds | 按 PublishedAt 计算最大年龄 |
| minVersions | 自动清理至少留下的最新 ready 版本数量，默认 1 |
| overflowAction | 创建数量超限时 reject 或 deleteOldest |
| protectAliases | 自动清理是否保护普通 alias 引用，默认 true |

maxVersions、maxAgeSeconds 必须为正数，minVersions 不得为负；同时设置数量上限时 minVersions 不能大于 maxVersions。

显式 Protected 始终阻止删除。普通 alias 引用阻止显式删除；自动策略依据 protectAliases 保护。关闭 protectAliases 时，自动淘汰需在同一事务内解除指向淘汰版本的 alias、清空 VersionID、设置 DeletedAt 并递增 Revision，保留可用于条件重建的墓碑，不能留下悬空引用。系统 latest 不产生额外保护，最低保留数量保证自动清理留下最新版本。

当前作用域内的调用方可以关闭 protectAliases，后台在自动淘汰时解除包括 production 在内的普通 alias；本版不分别检查策略和 alias 操作权限。该规则不改变显式 Protected 的删除保护。

数量与年龄任一条件命中可进入淘汰集合，再扣除显式保护、引用保护和最低保留集合。淘汰从 Sequence 最小的旧版本开始。为满足创建数量上限无法淘汰足够旧版本时，返回 RetentionBlocked，整个创建回滚。

年龄策略由后台扫描执行，不承诺达到年龄边界的瞬间删除。新发布的版本不参与同一次创建淘汰。minVersions 只约束自动清理，显式删除可删除最后一个未保护、未被引用的版本。

策略更新保存新规则并触发评估。降低上限后，无法清理受保护版本时记录阻塞原因，后续创建仍执行新上限。maxVersions 限制可见版本数，不是即时物理存储容量上限。

### 预览

PreviewRetention 返回候选版本、保留版本、命中规则与保护原因。预览不修改版本、alias 或文件。返回策略 Revision 与计算时间；真正执行时必须在锁内重新计算，不能按过期预览直接删除。

### 纯数据库版本删除

在 Artifact 锁内完成保护检查、状态转换和内容清除。删除后 Payload 与 Annotations 清空为 {}，保留版本 ID、编号、Sequence 与时间等墓碑信息，去重结果仍能指向它。

### 文件版本删除

~~~mermaid
stateDiagram-v2
    ready --> deleting: 逻辑删除并写入任务
    deleting --> deleting: 文件回收失败后重试
    deleting --> deleted: 文件回收完成并清除内容
~~~

在 artifact 本地事务标记版本 deleting，并写含完整 ReferenceID/FileID 的释放任务。普通查询立即隐藏版本；后台可靠调用 ReleaseReferences，storage 在自己的事务中释放并安排最后引用的物理回收。artifact 通过 GetReferenceStatuses 确认本版本各引用全部完成后，将版本标记 deleted 并清除 Payload 与 Annotations。VersionFile 保留必要内部引用身份供结果确认，清单对外已不可见。

不提供把 deleting 或 deleted 改回 ready 的恢复操作。保留版本墓碑及回收所需文件身份，直到确认回收；墓碑不保留原业务 payload。物理删除只针对最后引用已释放的StorageFile；重复删除不会影响仍有引用的文件。共享文件的其他版本或草稿不阻止当前版本完成逻辑删除与墓碑收尾。

## 多副本后台任务

artifact 的 BackgroundTask 负责版本保留评估、持久引用释放/补偿与等待释放后的版本收尾；storage 的 StorageTask 负责临时文件到期与物理回收。两者各自拥有 worker、任务表、租约和失败预算，不对外暴露 task ID 或任务管理 API。artifact worker 调用 ReleaseReferences 与 GetReferenceStatuses，并中止到期的 ReferenceChange，不直接访问 S3 或 storage 表；storage worker 不调用 version.FinishDeletion。所有 worker 均由公开 service 生命周期启动、取消并等待。

### 任务领取与完成

1. 短事务按 State/NextRunAt 使用 FOR UPDATE SKIP LOCKED 领取，写 running、LeaseToken、LeaseUntil 后提交。
2. storage 在事务外执行 Stat/Delete，长任务按 DB 时钟续租；文件状态变更与自身任务 done 同 tx 条件提交。artifact 在事务外可靠发送完整释放计划，按每项 NotBefore 调度，在 storage 尚未全部回执时安排下次检查；storage 等待 LastGrantExpiresAt 时推进 NextRunAt。两者均为正常等待，不累计 Attempts 或失败预算。
3. artifact 全部回执后，锁 Artifact、核验租约，FinishDeletion 与 artifact 任务 done 同事务提交。先前过期执行者不可覆盖新租约结果。
4. 失败记录 LastError/Attempts，按有上限的退避加抖动重试；预算耗尽为 failed 并告警。周期扫描不得无限复活 failed，人工重排流程在交付前落实。
5. 租约过期允许其他独立进程接管；外部删除至少一次、幂等。无全局 leader。

任务按 Kind/TargetID 唯一；引用任务 TargetID 是 AttemptID 或独立删除交接 ID，Payload 不可变，保存完整引用对、可信 scope、每项 NotBefore 及收尾目标，不用逻辑请求键覆盖。归属状态始终在锁内重查。跨文件操作按 FileID 升序；storage 文件相关 worker 一律先文件后任务，artifact 收尾先 Artifact 再任务，领取任务的短事务不同时获取目标资源锁，避免反向持锁。不能在外部 I/O 期间持业务锁。

### 扫描边界

artifact 扫描到期 preparing，在短事务以当前 AttemptID/状态条件中止并写全计划补偿，不代替客户端保存或发布。artifact 也扫描 NextRetentionAt 到期集合，在 Artifact 锁内重新计算 Plan，应用 MarkDeleting 并推进下一时间。storage 扫描未绑定且过期的 uploading/ready 文件，锁内复查再安排删除；bound 永远不按上传期限回收。终态位置墓碑按 NextSweepAt 分批检查迟到对象，不借扫描绕过失败预算。

租约、文件期限和年龄判断使用数据库时钟；期限检查使用取锁后的当前时间，不能复用长事务开始时已陈旧的时间。未完成分片回收的实际后端能力与最长允许上传时间共同验收。

## Redis 的可选用途

第一版只依赖数据库即可实现编号、幂等和任务协调。Redis 后加时：

- 可缓存不可变快照，但返回前仍检查身份和作用域并从主库确认版本可见性。
- alias 与 latest 的强一致解析走主库。
- Streams 可分发任务通知，数据库任务记录继续作为事实来源。
- 数据库事务只写任务；事务外发送 Redis 通知，丢失通知由数据库扫描补偿。
- 消费者允许重复消息，领取任务时仍使用数据库状态和租约。

Redis 故障不会影响编号正确性，数据库 worker 可以继续执行。不会用 Redis INCR 或分布式锁代替 Artifact 行锁。

参考：[Redis Streams](https://redis.io/docs/latest/develop/data-types/streams/)。

## API 与 SDK

本节共有27项HTTP操作：artifact 21项（含草稿CRUD5项），storage 文件4项、统一grant 1项、租户bucket管理1项。artifactv1 与 storagev1 是同一 Go module 内两个独立 SDK 包。storage 用户文件接口使用普通可信身份与精确scope；bucket管理独立使用 TenantServiceVerifier 验证租户系统，不接受普通用户身份代替。

统一 HTTP 前缀为 /v1，API 路径不包含 TenantID 或 ProjectID。TenantID、ProjectID 从 Middleware 建立的可信请求 context 取得；创建集合时必填 type，请求体若提供 ProjectID 必须与 context 一致。已有集合的归属和类型由数据库记录决定，列表按 context 的确定作用域过滤，可按 type 筛选。artifact:// URI 保留租户、项目及类型层级；存储 key 由 storage 独立生成。

| 方法与路径 | 行为 |
| --- | --- |
| POST /v1/artifacts | 在 context 指定的作用域创建集合，请求体必填 type，ProjectID 若提供须一致 |
| GET /v1/artifacts | 按 context 指定的作用域列出集合，可按 type 筛选 |
| GET /v1/artifacts/{artifactID} | 获取集合类型、归属、模式与状态 |
| POST /v1/artifacts/{artifactID}/archive | 停止新发布，保留历史读取与编号身份 |
| POST /v1/artifacts/{artifactID}/versions | 发布纯数据库或带文件版本 |
| GET /v1/artifacts/{artifactID}/versions | 按 Sequence 游标分页，可按编号筛选 |
| GET /v1/artifacts/{artifactID}/versions/{versionID} | 读取固定快照 |
| PATCH /v1/artifacts/{artifactID}/versions/{versionID}/annotations | 修改管理信息，检查 Revision |
| PATCH /v1/artifacts/{artifactID}/versions/{versionID}/protection | 集合管理者设置或解除 Protected，检查作用域和 Version.Revision |
| DELETE /v1/artifacts/{artifactID}/versions/{versionID} | 逻辑删除；带文件时返回 202 |
| PUT /v1/artifacts/{artifactID}/aliases/{name} | 条件创建或移动 alias |
| DELETE /v1/artifacts/{artifactID}/aliases/{name} | 按 expectedRevision 删除 alias |
| GET /v1/artifacts/{artifactID}/aliases/{name} | 返回 alias 修订号及固定版本身份；includeDeleted=true 可读取当前作用域内的墓碑修订号 |
| PUT /v1/artifacts/{artifactID}/retention-policy | 条件更新策略 |
| POST /v1/artifacts/{artifactID}/retention-policy/preview | 预览保留结果 |
| POST /v1/resolve | 解析资源 URI |
| POST /v1/storage/files | 请求为顶层登记项数组；200为顶层逐项结果数组 |
| GET /v1/storage/files/{fileID} | 查询文件元数据、状态与期限，200 |
| POST /v1/storage/grants | fileIDs、scope预设/Actions、可选ttlSeconds；storage独立按归属与状态申请/续发一份STS，200整批成功 |
| POST /v1/storage/files/complete | 请求为顶层FileID字符串数组；200为顶层逐项结果数组 |
| DELETE /v1/storage/files/{fileID} | 删除未绑定文件，202；已deleted为200；bound为409 |
| POST /v1/storage/tenant-buckets | 租户系统创建空间；新建201，确认既有200 |
| POST /v1/artifacts/{artifactID}/drafts | requestKey幂等创建；首次201，重复200，原草稿已删410 |
| GET /v1/artifacts/{artifactID}/drafts | 返回DraftSummary列表，不包含JSON或文件清单 |
| GET /v1/artifacts/{artifactID}/drafts/{draftID} | 读取指定草稿 |
| PUT /v1/artifacts/{artifactID}/drafts/{draftID} | requestKey幂等、按expectedRevision完整替换JSON及files；返回appliedRevision和一致draft |
| DELETE /v1/artifacts/{artifactID}/drafts/{draftID} | 按query Revision删除并持久记录异步释放义务，204 |

文件登记的 requestKey 必填，幂等范围为可信 TenantID、ScopeKey、ActorID 和 requestKey；size、规范化 contentType、声明摘要参与参数比较，省略/null摘要等价。命中返回原FileID与当前状态，不创建新文件或延长期限。

整数或语义化编号通过 versions 的 number 筛选查询，避免把 alias、编号与 VersionID 混放在一个未区分的路径参数中。

Go SDK 方法均接收 context.Context，JSON 契约使用 json.RawMessage 或业务定义结构。上传流程返回短期凭证和 S3 连接参数，调用方通过 S3 SDK 向指定对象上传，再调用确认接口；artifact-go SDK 不实现分片调度。读取授权返回相同GrantResponse，调用方使用STS与文件位置通过S3 SDK读取；两个SDK均不代理文件字节。

### Go 类型与接口契约

对象接口与持久化模型用 Go struct 声明；登记/确认的批量请求及成功响应用具名切片表示顶层数组。上面的 GORM struct 负责数据库表、索引和外键；本节类型负责 HTTP 与 SDK 契约，使用 json、uri、form 标签，不依赖 GORM。Controller 与 SDK 共用这些类型，通过显式映射转换持久化模型，不直接序列化数据库关联对象。

以下 artifact 契约位于 sdk/go/artifactv1，按 artifact、version、draft、alias、retention、resolve 分文件，version和draft不承担文件授权。storagev1 单独声明文件、统一grant与bucket契约，文件状态及 STS 凭证不进入 artifact SDK；两包共享协议格式，不互相引用内部类型。路径参数标记 json:"-"，查询参数用 form；时间 UTC RFC3339，空列表为 []，成功对象不包 data。

#### 请求身份与 SDK 构造

SDK 每次请求从 context 读取 RequestIdentity，把 BearerToken 写入 Authorization: Bearer，把 TenantID、ProjectID 分别写入 X-Tenant-ID、X-Project-ID；ProjectID 为空时省略对应 Header，表示租户直属范围。ResourceUserID 非 nil 时写入 X-Resource-User-ID，省略表示共享范围，不自动使用当前操作者。RequestID 可选，写入 X-Request-ID。Client 不保存请求身份，也不在并发请求时修改共享默认 Header。

这些 Header 是作用域选择输入，不是身份或成员关系证明。接入方 Middleware 验证 Bearer 身份并确认作用域来源可信，再建立服务端 context；受信网关注入作用域时须清除外部同名 Header。artifact-go 仍不检查业务操作权限。

~~~go
package artifactv1

import (
	"encoding/json"
	"time"
)

// 字符串枚举使用具名类型；Artifact.Type 仍是开放的业务字符串。
type VersionScheme string

const (
	VersionSchemeInteger VersionScheme = "integer"
	VersionSchemeSemver  VersionScheme = "semver"
)

type VersionBump string

const (
	VersionBumpMajor VersionBump = "major"
	VersionBumpMinor VersionBump = "minor"
	VersionBumpPatch VersionBump = "patch"
)

type OverflowAction string

const (
	OverflowReject       OverflowAction = "reject"
	OverflowDeleteOldest OverflowAction = "deleteOldest"
)

type ChecksumType string

const (
	ChecksumFullObject ChecksumType = "FULL_OBJECT"
	ChecksumComposite  ChecksumType = "COMPOSITE"
)

type ArtifactState string

const (
	ArtifactActive   ArtifactState = "active"
	ArtifactArchived ArtifactState = "archived"
)

type VersionState string

const (
	VersionReady    VersionState = "ready"
	VersionDeleting VersionState = "deleting"
	VersionDeleted  VersionState = "deleted"
)

type ClientConfig struct {
	Endpoint string
	Timeout  time.Duration // 0 使用框架 SDK 默认超时。
}

type RequestIdentity struct {
	BearerToken string
	TenantID    string
	ProjectID   *string // nil 表示租户直属范围。
	RequestID   string
}

// 公共字段按实际语义复用；路径字段不进入请求体。
type ArtifactPath struct {
	ArtifactID string `json:"-" uri:"artifactID" binding:"required" example:"a01"`
}
type VersionPath struct {
	ArtifactPath
	VersionID string `json:"-" uri:"versionID" binding:"required" example:"v01"`
}
type AliasPath struct {
	ArtifactPath
	Name string `json:"-" uri:"name" binding:"required,max=64" example:"production"`
}
type PageQuery struct {
	Cursor string `json:"-" form:"cursor" example:"opaque-cursor"`
	Limit  int    `json:"-" form:"limit" default:"50" binding:"min=1,max=200" example:"50"`
}

type CreateArtifactRequest struct {
	Type           string        `json:"type" binding:"required,min=1,max=64" example:"model"`
	Name           string        `json:"name" binding:"required,max=128" example:"demo"`
	ProjectID      *string       `json:"projectID,omitempty" binding:"omitempty,max=64" example:"p01"`
	VersionScheme  VersionScheme `json:"versionScheme" binding:"required,oneof=integer semver" example:"semver"` // integer | semver
	InitialVersion string        `json:"initialVersion,omitempty" binding:"omitempty,max=64" example:"1.0.0"`
}
type ListArtifactsRequest struct {
	PageQuery
	Type  string        `json:"-" form:"type" binding:"omitempty,max=64" example:"model"`
	State ArtifactState `json:"-" form:"state" binding:"omitempty,oneof=active archived" example:"active"` // 省略时包含 active、archived。
}
type GetArtifactRequest struct{ ArtifactPath }
type ArchiveArtifactRequest struct{ ArtifactPath }

type AliasUpdate struct {
	Name             string `json:"name" binding:"required,max=64" example:"production"`
	ExpectedRevision *int64 `json:"expectedRevision" binding:"required,gte=0" example:"0"`
}
type CreateVersionRequest struct {
	// 与直接Payload/Annotations/Files字段的存在互斥，二者成对提供。
	DraftID *string `json:"draftID,omitempty"`
	ExpectedDraftRevision *int64 `json:"expectedDraftRevision,omitempty"`
	ArtifactPath
	RequestKey      string          `json:"requestKey" binding:"required,max=64" example:"request-001"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Annotations     json.RawMessage `json:"annotations,omitempty"`
	Bump            VersionBump     `json:"bump,omitempty" binding:"omitempty,oneof=major minor patch" example:"minor"`
	Files           []FileReference `json:"files,omitempty" binding:"omitempty,dive"` // 绑定层另查字段存在与null。
	Protected       bool            `json:"protected" example:"false"`
	Aliases         []AliasUpdate   `json:"aliases,omitempty" binding:"omitempty,dive"`
}
type ListVersionsRequest struct {
	ArtifactPath
	PageQuery
	Number string `json:"-" form:"number" binding:"omitempty,max=64" example:"1.0.0"`
}
type GetVersionRequest struct{ VersionPath }
type UpdateAnnotationsRequest struct {
	VersionPath
	ExpectedRevision *int64          `json:"expectedRevision" binding:"required,gte=0" example:"1"`
	Annotations      json.RawMessage `json:"annotations" binding:"required"` // 完整替换，不做隐式合并。
}
type UpdateProtectionRequest struct {
	VersionPath
	ExpectedRevision *int64 `json:"expectedRevision" binding:"required,gte=0" example:"1"`
	Protected        *bool  `json:"protected" binding:"required" example:"false"`
}
type DeleteVersionRequest struct{ VersionPath }

type PutAliasRequest struct {
	AliasPath
	VersionID        string `json:"versionID" binding:"required" example:"v01"`
	ExpectedRevision *int64 `json:"expectedRevision" binding:"required,gte=0" example:"1"`
}
type DeleteAliasRequest struct {
	AliasPath
	ExpectedRevision *int64 `json:"-" form:"expectedRevision" binding:"required,gte=0" example:"1"`
}
type GetAliasRequest struct {
	AliasPath
	IncludeDeleted bool `json:"-" form:"includeDeleted" example:"false"`
}

type FileReference struct {
    Path string `json:"path" binding:"required,max=512"`
    FileID string `json:"fileID" binding:"required"`
}
type FileChecksum struct {
    Algorithm string `json:"algorithm"`
    Type ChecksumType `json:"type"`
    Value string `json:"value"`
}
type RetentionRules struct {
	Enabled        bool           `json:"enabled"`
	MaxVersions    *int64         `json:"maxVersions"`
	MaxAgeSeconds  *int64         `json:"maxAgeSeconds"`
	MinVersions    int64          `json:"minVersions"`
	OverflowAction OverflowAction `json:"overflowAction"` // reject | deleteOldest
	ProtectAliases bool           `json:"protectAliases"`
}
type UpdateRetentionRequest struct {
	ArtifactPath
	ExpectedRevision *int64         `json:"expectedRevision" binding:"required,gte=0" example:"1"`
	Enabled          *bool          `json:"enabled" binding:"required" example:"true"`
	MaxVersions      *int64         `json:"maxVersions" binding:"omitempty,gt=0" example:"10"`
	MaxAgeSeconds    *int64         `json:"maxAgeSeconds" binding:"omitempty,gt=0" example:"86400"`
	MinVersions      *int64         `json:"minVersions" binding:"required,gte=0" example:"1"`
	OverflowAction   OverflowAction `json:"overflowAction" binding:"required,oneof=reject deleteOldest" example:"deleteOldest"`
	ProtectAliases   *bool          `json:"protectAliases" binding:"required" example:"true"`
}
type PreviewRetentionRequest struct{ ArtifactPath }
type ResolveRequest struct {
	URI string `json:"uri" binding:"required" example:"artifact://t01/artifacts/model/a01/versions/v01"`
}

// 对外响应不包含 ScopeKey、存储管理凭据或 GORM 关联字段。
type ArtifactResponse struct {
	ID              string            `json:"id"`
	TenantID        string            `json:"tenantID"`
	ProjectID       *string           `json:"projectID"`
	OwnerUserID     *string           `json:"ownerUserID"` // 共享为 null，与 createdBy 分开。
	Type            string            `json:"type"`
	Name            string            `json:"name"`
	ArtifactURI     string            `json:"artifactUri"`
	VersionScheme   VersionScheme     `json:"versionScheme"`
	InitialVersion  string            `json:"initialVersion"`
	State           ArtifactState     `json:"state"`
	CreatedBy       string            `json:"createdBy"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	RetentionPolicy RetentionResponse `json:"retentionPolicy"`
}
type ListArtifactsResponse struct {
	Items      []ArtifactResponse `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}
type VersionIdentity struct {
	ID         string `json:"id"`
	ArtifactID string `json:"artifactID"`
	Version    string `json:"version"` // 对应数据库 Number。
	Sequence   int64  `json:"sequence"`
	VersionURI string `json:"versionUri"`
}
type FileResponse struct {
    FileID string `json:"fileID"`
    Path string `json:"path"`
    FileURI string `json:"fileUri"`
    Size int64 `json:"size"`
    ContentType string `json:"contentType"`
    DeclaredChecksum *FileChecksum `json:"declaredChecksum"`
}
type VersionResponse struct {
	VersionIdentity
	Payload     json.RawMessage `json:"payload"`
	Annotations json.RawMessage `json:"annotations"`
	Protected   bool            `json:"protected"`
	Revision    int64           `json:"revision"`
	State       VersionState    `json:"state"`
	Files       []FileResponse  `json:"files"`
	CreatedBy   string          `json:"createdBy"`
	PublishedAt time.Time       `json:"publishedAt"`
}
type ListVersionsResponse struct {
	Items      []VersionResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}
type DeleteVersionResponse struct {
	VersionIdentity
	State  VersionState `json:"state"`  // 无文件为 deleted；带文件为 deleting。
}
type AliasResponse struct {
	ArtifactID string           `json:"artifactID"`
	Name       string           `json:"name"`
	Revision   int64            `json:"revision"`
	Target     *VersionIdentity `json:"target"` // 墓碑为 null。
	DeletedAt  *time.Time       `json:"deletedAt"`
}
type RetentionResponse struct {
	ArtifactID string `json:"artifactID"`
	RetentionRules
	Revision  int64      `json:"revision"`
	UpdatedAt *time.Time `json:"updatedAt"` // 尚未配置时为 null，Revision=0。
}
type RetentionDecision struct {
	Version VersionIdentity `json:"version"`
	Reasons []string        `json:"reasons"`
}
type PreviewRetentionResponse struct {
	PolicyRevision  int64               `json:"policyRevision"`
	CalculatedAt    time.Time           `json:"calculatedAt"`
	Candidates      []RetentionDecision `json:"candidates"`
	Retained        []RetentionDecision `json:"retained"`
	Blocked         bool                `json:"blocked"`
	BlockingReasons []string            `json:"blockingReasons"`
}
type ResolveResponse struct {
	Artifact ArtifactResponse `json:"artifact"`
	Version  *VersionResponse `json:"version"`
	File     *FileResponse    `json:"file"`
}

// SDK 中的类型化错误，不作为 JSON 请求或响应直接序列化。
type VersionDeletedError struct {
	Cause   error // 保留已解析的框架 errors.Error。
	Version VersionIdentity
}

type DraftIdentity struct {
	ArtifactID string `json:"artifactID"`
	ID string `json:"id"`
}

// 与 server-go 标准错误字段保持一致；410 可增加版本或草稿身份。
type ErrorResponse struct {
	Code       string           `json:"code"`
	Message    string           `json:"message,omitempty"`
	RequestID  string           `json:"requestId"`
	StatusCode int              `json:"statusCode"` // 框架错误编号，不是 HTTP 状态。
	Version    *VersionIdentity `json:"version,omitempty"`
	Draft      *DraftIdentity   `json:"draft,omitempty"`
	// 仅已成功保存草稿的重放在草稿删除后携带。
	AppliedRevision *int64 `json:"appliedRevision,omitempty"`
}
~~~

#### storagev1 契约

storage SDK 自有 ClientConfig、逐请求身份输入及错误解码，不依赖 artifactv1。普通用户请求走接入方 IdentityResolver；CreateTenantBucket 用租户系统服务身份，由 TenantServiceVerifier 验证。凭证不写日志或普通资源响应。

| 类型 | 字段与语义 |
| --- | --- |
| RegisterFileItem | requestKey必填≤64；size必填非负；contentType省略默认application/octet-stream；declaredChecksum可选，提供时algorithm/type/value完整；不接受path、ArtifactID、bucket或owner |
| FilePath | fileID，仅路径参数 |
| GetFileRequest / DeleteFileRequest | FilePath；无JSON body |
| RegisterFilesRequest | 具名切片 `[]RegisterFileItem`；JSON顶层对象数组，1–1000项，无files包装 |
| CompleteFilesRequest | 具名切片 `[]string`；JSON顶层FileID数组，1–1000项，无fileIDs包装 |
| CreateGrantRequest | fileIDs数组1–1000唯一项；scope必填；ttlSeconds可选正整数；不包含业务来源或owner |
| GrantScope | preset与actions按字段存在严格二选一；preset为upload/download，actions为S3原名非空唯一数组 |
| RegisterFilesResponse / CompleteFilesResponse | 各自为逐项结果的具名切片；JSON顶层数组，与输入等长且同顺序，逐项成功字段或error(code/message/status)，登记带requestKey，确认带fileID；不包files/items/data |
| FileResponse | fileID、state、expectedSize、size（确认前null）、contentType、declaredChecksum、createdAt、expiresAt；不含物理位置、凭证或可作为授权的owner输入 |
| GrantResponse | endpoint、region、forcePathStyle、expiresAt、actions；credentials{accessKeyID,secretAccessKey,sessionToken}；files[{fileID,bucket,objectKey}]按输入顺序；整批一份STS，不含逐项error |
| CreateTenantBucketRequest | tenantID必填，只允许可信租户系统调用 |
| TenantBucketResponse | tenantID、backendID、bucket，无管理密钥 |

storage FileState 使用具名字符串枚举 uploading/ready/bound/deleting/deleted。声明摘要可选但一旦登记即固定，不因重复请求更改。FileResponse 的 expiresAt 表示未绑定截止时间；bound 后保留原值供审计，不表示将被自动过期删除。DeleteFile 返回 FileResponse，不暴露 taskID。

SDK 方法为 RegisterFiles、GetFile、CreateGrant、CompleteFiles、DeleteFile、CreateTenantBucket，均接收标准 context 与对应请求类型，和六条 storage 路由对应；RegisterFiles/CompleteFiles 直接序列化请求切片并解码成功结果切片，不在 SDK 添加包装。artifactv1不保留文件授权方法；读取通过storagev1.CreateGrant按文件身份作用域、状态与期限检查后签发，不依赖artifact，也不需要额外公开票据。

登记和确认的 OpenAPI requestBody 与 HTTP 200 schema 均为 `type: array`，items 分别为登记项对象/FileID字符串及对应逐项结果对象；请求 minItems=1、maxItems=1000，确认 FileID 和登记 requestKey 的唯一性按业务契约校验。非200响应仍引用标准错误对象。Controller 直接绑定顶层数组，有界读取与数组结构/长度/重复检查先于逐项执行；数组不依赖外层 struct 的字段标签表达整批约束。登记项默认值及字段参数校验仍按逐项规则处理，不因一次绑定器校验失败吞掉批量结果。SDK、Controller 和 OpenAPI 必须保持相同 JSON 形状。

### 其他接口的包装审查

本轮核对全部27项公开操作。登记和确认都是没有批次级参数的纯批量输入/输出，使用顶层数组。其余对象结构有当前语义，不增加统一 data 包装：

- grants 同时包含 fileIDs、scope、ttlSeconds；响应同时包含凭证、连接参数、期限及 files，保留对象结构。
- 草稿与版本的 files 与 JSON、修订、请求键等并列，保留 files 字段；草稿保存响应的 appliedRevision 与当前 draft 是不同事实，保留两者。
- 集合、版本及草稿列表包含 items 和 nextCursor，保留分页对象；保留策略预览同时返回候选、保留项、阻塞原因及策略修订，不压成一个数组。
- Resolve 可同时返回 artifact、version、file，保留具名结果；租户空间创建的 tenantID、Resolve 的 uri 等是命令字段，不改成含义不明的裸字符串。资源响应直接返回对象，归档及已有 GET/DELETE 无需增加请求体，草稿删除204保持无响应体。

#### 参数、结果与错误语义

- 创建集合的 versionScheme 必填；initialVersion 省略时按模式补为 1 或 1.0.0。type、name 必填并使用前述规范值。ProjectID 不能改变 context 的作用域。
- 发布 requestKey 必填；payload、annotations 省略时为 {}，提供时必须是 JSON 对象。直接发布的 files 省略或 [] 表示无文件，显式 null 拒绝；带文件提供 path/fileID 清单。草稿来源禁止出现 files，即使为空。protected 默认 false，整数模式禁止 bump；alias 列表不允许同名项。
- 列表 limit 默认 50、范围 1 至 200。集合按 ID 升序，版本按 Sequence 降序；cursor 是服务生成的游标，绑定查询作用域与筛选条件。nextCursor 为空表示结束，客户端不能将游标用于另一作用域或筛选条件。列表反映查询时的当前数据，不承诺跨页快照。
- expectedRevision 对修订操作必填，用 *int64 区分缺失的 nil 与显式 0；Controller 拒绝 nil。alias 从未存在、保留策略尚未配置时允许显式 0；其他更新使用当前 revision。DELETE alias 将 expectedRevision 放入 query，不使用 DELETE body。
- UpdateRetentionRequest 完整替换策略，所有 RetentionRules 字段必须出现；maxVersions、maxAgeSeconds 可为 null；Controller 对这两个键执行字段存在检查，缺失拒绝，显式 null 接受，不能用 required 指针拒绝合法 null。启用的策略须至少设置一个限制，minVersions 和 protectAliases 必须显式传入，避免遗漏布尔字段变成 false。
- ArchiveArtifactRequest 无 JSON body；storage 的授权、确认及删除按其独立契约。服务决定临时文件期限，客户端不能延长或指定任意 ObjectKey。
- ResolveResponse 在集合 URI 时仅 artifact 有值；版本或 alias URI 返回 artifact 与 version；文件 URI 同时返回 file，响应不包含文件字节。无法定位 ready 版本时返回前述领域错误。
- ArtifactResponse 同时返回当前 retentionPolicy，供调用方取得策略内容与修订号；未配置时 enabled=false、revision=0，其他规则采用服务默认值。AliasResponse 对普通 alias 返回真实 revision；latest 为计算结果，revision 为 0、deletedAt 为 null，只能读取，不能更新或删除。DeleteAlias 返回更新后的墓碑对象。
- 创建集合、首次创建草稿和首次发布返回201；草稿创建/发布成功重试200。批量登记和确认在结构及身份有效时统一200，逐项返回结果或错误；grant仅在整批授权成功时200，失败返回整体错误。无文件版本删除200，带文件版本删除202；storage删除状态码见路由表，草稿删除204；其他JSON成功200。
- ErrorResponse 复用 server-go 错误字段；VersionDeleted 的 HTTP 410 响应额外携带 version 身份，不包含 payload、annotations、files。该扩展由本服务 Controller 输出，不能经过会丢弃扩展字段的标准错误归一化后再序列化。SDK 保留框架 errors.Error，并用可 Unwrap 的 VersionDeletedError 提供 VersionIdentity；不扩大框架公共错误类型。普通 JSON 方法使用 sdk.Complete(response, err) 判断结果；410 从本次错误响应额外读取 version 并包装，不能只做普通错误解码后丢失身份。访问授权是普通JSON响应，沿用同一错误处理。网络错误与 context 取消保留原错误身份。

CreateDraftRequest 与 UpdateDraftRequest 均包含必填 requestKey；UpdateDraftResponse 为 {appliedRevision, draft: DraftResponse}。DraftSummary 字段为 id、artifactID、revision、createdAt、updatedAt，列表不复用完整 DraftResponse。DraftDeleted 的410扩展为 draft:{artifactID,id}；保存重放另含 appliedRevision，创建重放不提供该字段。SDK 以可 Unwrap 的 DraftDeletedError 保留框架错误、DraftIdentity 和可选 AppliedRevision，与 VersionDeletedError 使用相同传输规则，不新增通用错误包。

#### SDK 方法与路由对应

对象请求的静态约束放在 binding 标签中；Controller 对有 default 标签的对象请求在绑定前调用 defaults.Set(&req)，检查其错误，再用 binder.Bind(ctx, &req).All().Error() 完整合并 JSON、query 和路径来源并校验，通过 serrors.AbortWithError 返回错误。不能绑定后再次补静态默认值，也不为 PATCH 补值；initialVersion、payload、语义化 bump 等依赖业务规则的默认值留在 Service。响应不加请求 binding 标签。顶层数组接口按上述批量契约绑定和逐项校验，不套用对象字段绑定流程。每个 handler 的 Swagger 注释同步参数来源、必填、范围、默认值、成功与错误 schema，并由 make doc 生成 docs/openapi.json 与 docs/openapi.yaml。

下面是 Client 的方法签名契约，尚未实现 HTTP 调用。所有请求使用本次 ctx 的身份和取消信号；无返回对象时直接返回 error。Client.Close 释放其拥有的框架 SDK 资源；访问授权返回普通 JSON。方法签名中的 context 来自标准库。

~~~go
func NewClient(config ClientConfig) (*Client, error)
func WithRequestIdentity(ctx context.Context, identity RequestIdentity) context.Context
func (c *Client) Close() error
func (e *VersionDeletedError) Error() string
func (e *VersionDeletedError) Unwrap() error
func (c *Client) CreateArtifact(ctx context.Context, req *CreateArtifactRequest) (*ArtifactResponse, error)
func (c *Client) ListArtifacts(ctx context.Context, req *ListArtifactsRequest) (*ListArtifactsResponse, error)
func (c *Client) GetArtifact(ctx context.Context, req *GetArtifactRequest) (*ArtifactResponse, error)
func (c *Client) ArchiveArtifact(ctx context.Context, req *ArchiveArtifactRequest) (*ArtifactResponse, error)
func (c *Client) CreateVersion(ctx context.Context, req *CreateVersionRequest) (*VersionResponse, error)
func (c *Client) ListVersions(ctx context.Context, req *ListVersionsRequest) (*ListVersionsResponse, error)
func (c *Client) GetVersion(ctx context.Context, req *GetVersionRequest) (*VersionResponse, error)
func (c *Client) UpdateAnnotations(ctx context.Context, req *UpdateAnnotationsRequest) (*VersionResponse, error)
func (c *Client) UpdateProtection(ctx context.Context, req *UpdateProtectionRequest) (*VersionResponse, error)
func (c *Client) DeleteVersion(ctx context.Context, req *DeleteVersionRequest) (*DeleteVersionResponse, error)
func (c *Client) PutAlias(ctx context.Context, req *PutAliasRequest) (*AliasResponse, error)
func (c *Client) DeleteAlias(ctx context.Context, req *DeleteAliasRequest) (*AliasResponse, error)
func (c *Client) GetAlias(ctx context.Context, req *GetAliasRequest) (*AliasResponse, error)
func (c *Client) UpdateRetention(ctx context.Context, req *UpdateRetentionRequest) (*RetentionResponse, error)
func (c *Client) PreviewRetention(ctx context.Context, req *PreviewRetentionRequest) (*PreviewRetentionResponse, error)
func (c *Client) Resolve(ctx context.Context, req *ResolveRequest) (*ResolveResponse, error)
~~~

上述方法属于 artifactv1；storagev1 六方法按自己的路由对应；具体类型名不影响 HTTP 路径。Client 的传输实现及 VersionDeletedError 方法体留到 SDK 实施阶段，设计文档不提供占位运行代码。

草稿 SDK 另提供 CreateDraft、ListDrafts、GetDraft、UpdateDraft、DeleteDraft；UpdateDraft 返回 UpdateDraftResponse{appliedRevision,draft}，CreateVersion沿用发布方法并增加草稿来源；具体草稿契约以本文件“带文件的工作草稿”及代码结构设计为准。

### 主要错误

| HTTP 状态 | 领域错误 | 说明 |
| --- | --- | --- |
| 400 | InvalidVersionRequest | 参数、编号模式或初次 bump 无效 |
| 413 | PayloadTooLarge / AnnotationsTooLarge / RequestBodyTooLarge | 对应JSON字节数超过配置；不自动落文件 |
| 400 | FileLimitExceeded | 登记单文件大小或草稿/版本文件数超过部署配置上限 |
| 401 | Unauthenticated | 缺少有效身份或可信请求作用域 |
| 404 | ResourceNotFound | 当前作用域内没有目标；跨作用域资源同样返回 404 |
| 409 | RevisionConflict | 草稿、alias、管理信息或策略修订号不匹配 |
| 409 | IdempotentParameterMismatch | 相同请求键对应不同参数 |
| 400 | GrantTTLTooShort | 扣除5秒签发预算后不足后端最短900秒；不得向上取整越过原截止 |
| 409 | RequestInProgress | 原键请求仍在准备，附重试建议；原键原参核对 |
| 503 | OperationOutcomeUnknown | 无法从主库确认提交结果，保留引用，原键原参核对 |
| 409 | RetentionBlocked | 受保护版本导致无法满足数量上限 |
| 409 | FileNotBindable | 文件仍uploading、未引用且已过期或已进入回收；跨scope仍为404 |
| 409 | FileBound | 普通storage删除不能删除已绑定文件 |
| 410 | DraftDeleted | 创建或保存已成功但原草稿已删；携draft:{artifactID,id}，保存重放另含appliedRevision；无内容、不重建 |
| 410 | VersionDeleted | 已成功发布的请求对应版本已逻辑删除，状态为 deleting 或 deleted |
| 503 | DependencyUnavailable | 数据库或必要存储不可用 |

外部依赖错误转换为领域错误，保留 requestID。只有通过整批身份、作用域、Actions与文件状态检查的CreateGrant成功响应返回精确文件集合的限权STS；错误响应、普通资源响应和日志不包含凭证，服务自身的存储管理凭据不返回客户端。跨作用域访问统一返回 ResourceNotFound。

## server-go 工程组织

完整目录、逐文件职责及测试映射见[代码结构设计](codebase-design-20261014.md)。artifact 有六个 app：artifact、version、draft、alias、retention、resolve。internal/storage 是独立职责模块，包含文件、统一grant与bucket HTTP、绑定/释放、worker、自有 table 与 S3 client。artifact 的 internal/table 有十一张表，storage 的 table 有三张表，双方不直接读取对方表。

~~~mermaid
flowchart TB
    C[客户端] --> A[artifactv1 / 六个app]
    C --> S[storagev1 / 文件授权与租户空间]
    A --> AT[(artifact十一表)]
    A -->|预先持久 BindReferences| S
    AW[artifact后台保留与收尾] --> A
    AW -->|可靠 ReleaseReferences / GetReferenceStatuses| S
    S --> ST[(storage三表)]
    S --> SW[storage回收worker]
    SW --> ST
    S --> O[S3 / STS]
    SW --> O
    C -->|直接上传和读取| O
~~~

version 拥有发布事务；retention.Plan 为纯函数；alias/mutation 共用名称、Revision、墓碑与ready目标规则；version/deletion 拥有逻辑删除及 FinishDeletion。storage.BindReferences/ReleaseReferences/GetReferenceStatuses 是有实际状态约束的可信交接接口，拥有独立事务，不接收 artifact tx；ReferenceChange 与 BackgroundTask 保存持久决策和发送义务。artifact 不调用 storage HTTP handler，不建立通用 Repository 或 workflow 层。

各域本地事务采用 server-go v0.10.0 db.Transaction，显式 READ COMMITTED、有界重试和固定锁顺序；业务不另建 internal/dbtx。两个 table 包分别由 GORM 声明结构并 AutoMigrate，初始化失败停止启动，不维护历史版本迁移链；部署由一个初始化实例先建表。SDK不依赖GORM、Gin或internal。

公开 service/config.go、identity.go、build.go、service.go 装配六个 app、storage 与各自 worker；两域数据库分别配置或注入，允许指向同库但不共享事务，可信 consumer 身份独立于普通用户身份；独立入口使用 app.Run 与 app.Runtime，组合应用按所有权注入资源。artifact-only 装配仅依赖 artifact DB 和可信 storage 交接客户端，不要求 storage DB/S3 凭据；storage-only 仅依赖 storage DB、S3 与服务身份验证，不要求 artifact DB；组合模式才同时装配两域。RegisterRoutes 区分普通可信身份路由与租户系统管理路由；Start 启动 worker，Stop 取消并等待。框架先排空HTTP，再停全部后台，最后由所有者关DB；借用连接不关闭。没有包级默认客户端。

Controller 仅绑定/输出、使用SDK struct生成OpenAPI，普通错误复用serrors；版本与草稿410身份扩展分别由version/draft输出及artifact SDK保留错误链。不新增httperror包。身份resolver由接入方提供，不把裸Header当可信scope，不反调租户权限服务。

## PG 与 MySQL 兼容要求

- 使用有符号整数，避免 MySQL 专属 unsigned 和数据库 ENUM。
- JSON 字段由 datatypes.JSON 映射；高频查询字段单独建列。
- 时间统一 UTC，任务租约使用数据库时间。
- 编号、租约、配置修订号都由短事务处理。
- 必要唯一键包含墓碑行，不依赖部分索引或 NULL 相等语义。
- 固定 READ COMMITTED，双方均使用正式支持的行锁语句。
- 数据库差异集中在 GORM 建表配置、错误分类和必要的类型适配，不分叉业务规则。
- 本版使用 GORM 模型与 AutoMigrate 管理表结构，不单独维护版本化 SQL 脚本。AutoMigrate 不会自动删除模型中已移除的列；后续实际需要删列或数据转换时，再显式处理该变更。
- PostgreSQL 与 MySQL 分别运行相同的行为和多进程并发用例。

## 部署与运行

~~~mermaid
flowchart LR
    C[调用方与 SDK] --> LB[负载均衡]
    LB --> S1[server-go 副本一]
    LB --> S2[server-go 副本二]
    S1 --> AD[(artifact 主库)]
    S2 --> AD
    S1 --> SD[(storage 主库)]
    S2 --> SD
    C -->|文件直传| B[对象存储服务]
    S1 -->|授权与核验、回收| B
    S2 --> B
    S1 -. 可选 .-> R[(Redis)]
    S2 -. 可选 .-> R
~~~

API 副本无本地业务状态，storage 文件状态在 storage 数据库，业务去重和引用决策在 artifact 数据库，两域任务各自持久保存；可同库部署但事务始终独立。文件上传流量直接进入存储服务。数据库读副本只用于明确允许延迟的统计，不用于编号、alias、生命周期和归属相关读取。

每个副本设置 worker 并发数、任务租约、续租周期、数据库连接池、上传授权有效期和 API 流量上限；同一部署的 MaxPayloadBytes、MaxAnnotationsBytes、MaxRequestBodyBytes、MaxFileBytes 与 MaxFilesPerVersion 保持一致。退出时取消本进程未完成 I/O，未确认任务由租约机制交给其他副本；客户端与存储服务之间的上传独立于 API 副本生命周期。数据库结构初始化、数据库备份和文件备份由部署流程统一执行，恢复时校验版本记录与对象位置一致。

运行观测至少覆盖发布成功与冲突、Artifact 锁等待、身份与作用域检查失败、上传失败、任务积压、回收失败、孤立对象与保留阻塞。日志关联 requestID、TenantID、ProjectID、ArtifactID、VersionID 和 TaskID。

## 验收标准

| 场景 | 必须观察到的结果 |
| --- | --- |
| 同租户同作用域同类型重名 | 唯一约束拒绝；租户级 NULL 项目下同类型也不能重复 |
| 同租户同作用域不同类型同名 | skill/demo 与 model/demo 可同时创建并独立编号，按类型生成各自的持久引用 |
| 相同名称跨租户或不同项目 | 按 context 作用域独立访问 |
| 类型传入及持久定位 | 创建集合必须提供合法 type；持久 URI 包含该类型，storage key不依赖类型，修改引用中的类型不能改变集合类型，发布版本不能覆盖类型 |
| 暂缓操作权限校验 | context 不含 Permissions；同一有效作用域内不区分读、写、管理权限，也不调用租户权限接口 |
| 租户直属与项目作用域隔离 | context 的 ProjectID 为空时不能访问项目资源；不同项目不能互相访问 |
| 同用户跨作用域访问 | URI 不能切换 context；租户或项目不匹配时返回 404 |
| API 请求中的租户与资源归属 | API 路径不含租户或项目 ID；资源所属租户、项目必须与可信 context 一致，其他租户的资源 ID 或持久 URI 不能切换请求租户 |
| 租户或项目删除 | 不级联删除物料、不自动解除保护或 alias；不再允许新上传与发布，既定保留策略及回收任务继续执行 |
| 遗留物料维护 | 接入方提供历史作用域 context，资源归属须匹配，删除仍检查保护和引用；管理员角色判定不在本版验收范围 |
| 纯数据库版本创建与回滚 | 保存完整快照，files 为 []，alias 指向准确旧内容 |
| 无文件版本清理 | 原 payload 被清除，墓碑编号保留 |
| major、minor、patch | 正确递增与低位归零，失败事务不推进编号 |
| 多副本同时创建 | 不重复编号，不覆盖快照，按集合独立并发 |
| 相同 requestKey 跨副本重试 | 返回同一结果身份；参数不同为 409 |
| 提交后响应丢失 | 重试不创建第二个版本 |
| 发布成功、响应丢失后集合归档 | 身份及作用域检查通过后，相同请求仍返回原版本身份；不再发布、不移动 alias，已删除结果按删除契约返回 |
| alias 并发更新与删除重建 | 修订号冲突被识别，旧请求不能覆盖新引用 |
| alias 墓碑读取与重建 | 管理者可读取墓碑 Revision 并条件重建；expectedRevision=0 不能重建墓碑，普通解析不返回已删除目标 |
| 文件绑定与过期回收并发 | 使用同一文件锁；绑定先提交则跳过过期回收，回收先转deleting则发布失败 |
| 多文件发布与引用隔离 | 一文件失败则业务不提交，已绑定批次按全部计划补偿；重复path/FileID拒绝；错误consumer或作用域释放及公开删除bound文件拒绝 |
| 两域回收交接 | 逻辑删除与不可变释放任务在artifact本地原子提交；后台跨服务幂等Release；共享文件该引用完成释放，最后引用等待物理删除回执；迟到对象独立清理 |
| 读取授权与版本删除 | 文件按可信精确作用域独立授权；删除A隐藏其清单并释放引用，B仍持F时同scope已知FileID仍能新申请读取；最后引用释放转deleting才阻止新授权；既有凭证自然到期 |
| 对象不存在或大小不匹配 | 文件不可ready或发布；服务不验证摘要，同大小错误内容由客户端负责 |
| 单次与分片上传的声明摘要 | 响应使用declaredChecksum，只记录客户端声明；不以此声称实际内容已验证，不额外读取文件 |
| 同 key 并发写入 | 由客户端协调；服务不把防覆盖作为接入条件，登记记录不被覆盖 |
| 客户端直传与伪造完成通知 | 文件字节不经过 artifact-go；存储对象不存在或核验不通过时不能就绪或发布 |
| 已发布后重用上传授权 | 不再签发新授权，既有凭证自然到期；外部对象变化由客户端负责，服务不复制封存或撤销凭证，JSON 与文件清单保持不变 |
| 租户 bucket 隔离 | 一租户一 bucket，同租户项目共用；bucket尚未创建或位置归属不符拒绝文件操作；A 租户凭证不能访问 B 租户 bucket；历史文件读取和回收使用记录的 Bucket |
| 短期上传凭证权限与续发 | 不能写入其他 key、删除完整对象或修改存储策略；凭证过期后失效，续发重新检查身份、作用域及文件状态 |
| 旧上传请求确认失败 | 不直接删除完整对象，不影响其他请求已确认或已发布的文件 |
| 本地部署与 S3 兼容存储 | 客户端可访问直传端点；限权短期凭证、租户 bucket 隔离、存在和大小核验与回收通过同一组存储契约测试 |
| 登记与发布限制 | storage拒绝超过MaxFileBytes的登记，artifact拒绝超过MaxFilesPerVersion的清单；登记同键相同参数返回同FileID，不延长TTL |
| 存储实际接收到超限文件 | 文件核验不能通过、不能发布；完整对象在文件终止后回收，不将登记限制宣称为即时物理容量限制 |
| 保留受保护版本 | 显式保护有效，alias 保护遵循配置 |
| 发布时直接保护版本 | protected=true 不额外检查操作权限；保护与发布原子提交，后续发布不能先于保护生效淘汰该版本 |
| 初始保护参数的幂等重试 | 同一请求键改变初始 protected 返回冲突；版本保护后续被修改，不影响相同发布请求的去重判断，也不被重试恢复 |
| 版本保护与并发修改 | 当前作用域内可设置或解除保护，修改必须匹配 Version.Revision；Protected 仍阻止删除 |
| 关闭 alias 保护并淘汰目标 | alias 在同一事务内解除，不留下悬空引用 |
| 策略与 alias 操作 | 当前作用域内可修改策略和 alias，不检查独立操作权限；Revision 和引用约束仍生效 |
| 逻辑删除后文件回收失败 | 版本隐藏，任务可观察并可重试 |
| 回收尚未完成时重试已成功发布请求 | 返回 410 及原版本身份，不返回已隐藏版本的 payload 或文件清单，不再次发布 |
| worker 崩溃与旧租约恢复 | 新副本接管，旧执行者不能确认新任务结果 |
| 迟到上传 | 数据库不重新发布，未引用文件能被回收扫描发现 |
| 草稿创建响应丢失 | 同键跨副本返回原ID及当前一致快照200，不新增引用；异参409，编辑后仍能确认，硬删后410无内容不复活 |
| 草稿历史清理 | 归档集合及归属已删除时，历史精确scope仍可CAS删除草稿；共享文件保留，最后引用回收；创建/编辑仍拒绝 |
| 草稿读取一致性 | Get与Put/Delete交错只返回同一Revision的JSON/清单或404；列表无大内容；写响应对应其提交快照 |
| JSON大小 | 三项限制在边界值允许、超过1字节413；覆盖多字节UTF8、转义、空白、数字词法、复制和草稿发布；失败无DB副作用，成功幂等不按当前快照重验 |
| 显式JSON文件 | 超内联限内容通过普通file保存/复用/回收；GET不隐式还原payload，内容修改新FileID并检查Revision |
| 统一Grant预设与自定义 | upload/download与显式Actions生成等价权限；二选一、非空唯一、未知/通配拒绝；响应包含最终actions，一批一份STS；同一S3动作不能因自定义绕过身份归属与文件状态 |
| 上传STS最小范围 | 只允许整批精确key的PUT及必要multipart/ListParts/Abort；拒读/HEAD/删完整对象/列桶/批外key与bucket；续发能恢复已知UploadID，所有目标授权上限单调不减 |
| 读取STS最小范围 | 只允许所选文件精确key的GET/HEAD/Range，拒写/删/列桶/未选文件；跨scope或不存在404；只读不延迟GC |
| ready文件读取与混批 | 确认后无需草稿/版本即可读取；ready到期拒绝，bound忽略历史准备TTL；同scope/backend的ready/bound及不同业务来源可同批，取全部适用期限最小值；读取不新增引用或延长准备期 |
| Grant整批失败与期限 | 任一文件失败不签发；混合读写409；策略过大明确拆批不扩大权限；失败或写签发后任一状态改变不返回凭证且保留所有预留期限；ttl取最早边界，不向上取整 |
| 带文件草稿持久化 | 保存JSON与files后刷新完整恢复；uploading拒绝挂入；保存引用后不因上传期限过期而回收 |
| 多页面草稿CAS | JSON与清单共同受Revision保护；确定冲突先aborted且不改变业务清单，新增引用按原准备窗口补偿；客户端保留本地内容并停止自动保存；进行中或结果未知原键核对 |
| 版本复制与重复发布 | sourceVersion复制清单并持有引用；同草稿不同requestKey发布复用FileID，文件字节无复制 |
| 草稿替换、改路径与删除 | 替换新FileID释放旧引用；同FileID改路径不释放；删除草稿后版本附件仍可读取 |
| 共享文件回收 | 两个Artifact同scope复用；释放一份不删除；最后释放安排GC；跨scope引用404；释放墓碑拒绝错误consumer且支持幂等；旧ReferenceID永不复活 |
| 发布与保留共享文件 | 先持久加新版本引用，再本地提交淘汰及释放任务，不产生零引用窗口；业务失败只回滚本地并可靠补偿新增引用 |
| 文件读取授权竞争 | 最后Release或ready过期/显式删除先转deleting则拒绝；非最后Release不禁读；授权先通过可在锁外签发，回收不延长签发期限，也不等待只读凭证 |
| 批量准备与失败重试 | 登记/确认请求及200响应均顶层数组，旧包装/null/非数组/非法元素结构拒绝且无副作用，1与1000项成功、空与1001项拒绝；结果等长同序，整请求错误仍为标准对象；逐项错误不回滚成功项，重复登记不延长TTL；grant整批失败无部分凭证；最终业务快照本地全原子，引用持久交接 |
| PG 与 MySQL | 相同结果、约束和失败行为 |

并发验收使用多个独立服务进程和真实 PostgreSQL/MySQL；引用交接至少一组运行两个服务进程、两个独立数据库。必须覆盖 intent 前后崩溃、Bind 响应丢失、Release 先到、分批部分成功、commit/abort 竞争、未知提交、同键新旧 attempt 隔离、CAS 失败附件窗口、来源变更、异步删除故障、GC 竞争及 consumer 隔离，完整判据见引用交接协议“故障验收”。Go race 检测只补充进程内检查。

## 实施顺序

1. 打通身份 context、四类精确作用域隔离、双数据库 GORM 建表及纯数据库版本的端到端读写。
2. 实现两种编号、持久幂等、alias 和集合级并发控制。
3. 实现保留预览、无文件清理和持久任务执行器。
4. 实现 S3 短期凭证授权与核验、登记规模限制、storage文件准备、持久引用交接发布、访问授权及两域回收交接，完成客户端分片上传验收。
5. 完成双数据库、多副本、崩溃恢复验证，再按实际吞吐需要加入 Redis。

纯数据库版本与文件版本共用发布事务和生命周期。文件扩展只增加上传、附件和存储操作，不产生第二套版本编号或 alias 逻辑。

## 部署与联调时确定的事项

- 本地开发采用文末T00确认参数；生产根据业务规模确定 MaxFileBytes、MaxFilesPerVersion、临时文件有效期、短期凭证有效期及未完成分片回收周期；不再引入租户容量账本。
- 选定并验证对象存储后端的临时授权接口、租户bucket定位规则与权限隔离、对象存在和大小核验，以及客户端可访问的端点和 CORS 配置。
- 按本节 HTTP/SDK Header 契约落实接入方 Middleware 的身份与可信作用域来源、归属删除联动和历史作用域维护入口；第一版不接入租户操作权限模型。


声明摘要的持久化：三个DeclaredChecksum字段整体为NULL或整体有值，由文件登记校验保证不接受不完整对象；空声明不生成虚构默认算法或空摘要。它们只供清单展示与调用方自查，不参与服务端内容验证或全局去重。

租户空间创建包含在27项HTTP操作中（21项artifact、4项storage文件、1项grant、1项管理），配套storagev1 SDK；具体路径、请求响应、状态码与TenantServiceVerifier装配见[代码结构设计的最终规则](codebase-design-20261014.md)。当前ID格式已核对forge-tenant-api实际源码，不从示例UUID推断。

## 已确认的 T00 实施参数

2026-10-15 用户批准 T00 全部建议，并授权原地初始化 Git、仅开始 T01；T02–T12 未启动。参数批准不等于对应业务能力已实现。完整依据见 [T00 review](t00-review-20261015.md)、[配置与受控恢复](t00-configuration-operations-20261015.md)、[内部 HTTP/JSON 契约](storage-internal-http-contract-20261015.md)。

开发环境固定：MaxFileBytes=1073741824（1 GiB），MaxFilesPerVersion=1000（草稿同限），文件准备期24小时；upload/download STS 默认及最大均1小时；attempt固定120秒不续期。内部连接/请求超时1秒/5秒，同步含初次最多3次、退避200/500ms加抖动；内部单批1–1000、请求体8 MiB。每域每副本worker并发2、租约60秒/续租15秒、扫描5秒、退避1–60秒加抖动、每轮最多20次实际失败；每域DB池MaxOpen=10/MaxIdle=5。只等待期限或回执不耗失败预算，失租/崩溃不推测执行失败。生产容量及凭据由部署环境提供。

JuiceFS 当前实测STS最短900秒、session policy上限2048字符。签发前在锁内固定截止D并为写授权预留LastGrantExpiresAt=max(old,D)；调用STS前以floor(D−当前时间−5秒)计算请求期限，不足900秒返回400 GrantTTLTooShort，不能向上取整。返回前校验当前时间<Expiration≤D；失败预留不缩短。多文件按最终policy长度检查，超限400 GrantScopeTooLarge由客户端拆批。只读授权不阻止回收。

内部四操作通过受保护独立HTTP入口：POST /internal/v1/storage/reference-files/query、/internal/v1/storage/references/bind、/internal/v1/storage/references/release、/internal/v1/storage/references/query。请求包含精确scope和数组，成功直接返回同序数组；Bind/Release批内原子。artifact→storage与tenant→storage使用分别配置的Bearer服务凭据，前者映射稳定ConsumerID和精确scope allowlist；跨主机或生产使用TLS，同机回环开发允许HTTP。用户IdentityResolver继续由接入层注入，T01测试可显式fixture，不能将普通Header或现有租户接口403当作可信历史deleted scope。

failed恢复由维护命令执行：CAS仅重排failed且无有效租约的任务，保留不可变载荷/ID/NotBefore、使旧token失效，NextRunAt=max(now,NotBefore)，开启新一轮预算；同事务写操作人、原因及前后状态。任务行增加RunGeneration、TotalAttempts、RecoveryEvents JSON，不增加第15张表或公共task API。实现及双库验证归T04/T07/T09。

正常文件回收等待写凭据期限后，按精确key枚举并Abort未完成分片、Delete对象并复查迟到写入；每小时扫描24小时以上分片仅作补漏，关联文件必须deleting/deleted且写授权已到期，未知key仅报告。该维护扫描使用服务身份，不授予客户端ListBucket权限，不以年龄删除活跃上传。现有trash-days=1保留，S3删除回执不表示SSD空间立即释放。自动回收及真实浏览器链路仍归后续验收。

### T01 HTTP绑定实现约束

server-go v0.10.0的JSON binder上限固定为1 MiB，与本项目8 MiB整包限制不同。T01 CreateArtifact使用标准库MaxBytesReader和JSON Decoder限制整包、拒绝未知字段及多个JSON值，再复用binding.Validator校验SDK标签；不新增通用binder。query/URI仍使用框架binder，依赖业务规则的默认值仍留在Service。

### T02 实施核对（2026-10-15）

T02只实现直接JSON版本的创建、列表、读取；首次发布201，成功重放200。带非空files、非空aliases、protected=true或草稿来源的发布请求返回400 InvalidVersionRequest，未开始T03及以后任务。字段名严格采用公开JSON契约拼写，拒绝大小写变体，避免字段存在/null检查与结构体解码不一致。版本不可见或跨归属返回404 ResourceNotFound，集合归档阻止新发布返回409 ArtifactArchived，编号溢出409 VersionNumberExhausted；成功回执的版本墓碑返回410 VersionDeleted及原身份。

Payload/Annotations以经过JSON校验的UTF-8字节保存，SDK使用json.RawMessage，持久模型使用[]byte确保驱动按二进制绑定，PostgreSQL使用bytea、MySQL使用longblob，不使用原生JSON数值转换。实际MySQL8.4探针表明原生JSON会把18446744073709551617及高精度小数转成double并改变值；本选择避免静默精度丢失，不在数据库内查询JSON字段。HTTP仍返回JSON对象，字段大小按原请求compact结果计量，指纹另按json.Number规范化；两者职责不变。

纯JSON发布采用READ COMMITTED、最多3次/5秒的明确回滚错误重试。事务回调成功但提交返回错误时，用独立最多5秒的只读主库查询核对scope、ActorID、requestKey和指纹：确认成功返回原版本/410，不能确认则503 OperationOutcomeUnknown。确认过程不分配编号、不重新提交；请求取消也只保留这一有界同步确认，不产生后台任务。客户端保留原键重试。

Version/CreateRequest在首期就建立集合归属外键、复合版本归属外键及Sequence/Number/请求键唯一约束，墓碑继续占位。MySQL新建表使用utf8mb4_0900_bin，保证请求键大小写与尾随空格有区别。当前只创建Artifact、Version、CreateRequest三张表，不提前创建后续表。

### T03 实施核对（2026-10-15）

本轮已完成alias三操作、注释与保护两操作，以及发布时alias/保护的原子提交，累计12个业务操作。此前T02“不支持aliases/protected”的记录仅描述当时切片，已由本节取代；files与草稿仍未实现。alias共享变更规则由调用方持有Artifact锁和事务；删除保留修订墓碑，latest为动态只读引用。注释和保护共享VersionRevision；expectedRevision显式0走409冲突，缺失/负数400。管理提交结果不明返回503并要求读取当前状态，未引入新的回执表。T04自动淘汰解绑及后续任务未开始。详见T03交付记录。

### T04 实施核对（2026-10-15）

T04已完成保留策略替换/预览、纯JSON版本删除，累计15个业务HTTP操作及对应SDK。发布按拟提交alias进行数量评估，与旧版淘汰同事务；年龄由后台按数据库时间评估。删除清内容、保留身份及去重回执，自动解除alias复用墓碑修订规则。持久BackgroundTask实现领取、续租、过期接管、失败预算及维护命令CAS重排审计；Service拥有worker启动、取消和等待。执行中只持Artifact业务锁，最终加任务锁核验token与租约，避免阻塞续租；提交结果不明不盲目增加失败预算。完整双库、独立进程、真实维护CLI、race/vet/build与OpenAPI检查通过。此前章节的T01/T02/T03状态为历史切片记录，本节为当前实施边界。T05及后续未开始，无storage/文件交接快捷实现。交付细节见docs/t04-acceptance-20261015.md。

### T05 实施核对（2026-10-15）

已完成纯JSON多草稿五操作、从ready版本复制及指定草稿修订发布，累计20个业务HTTP操作及对应SDK。草稿保存用ReferenceChange保存requestKey固定指纹、appliedRevision和空引用计划；创建另保留DraftCreateRequest，草稿硬删不删除两类回执。重放返回当前完整草稿和原保存修订，硬删后返回410身份。CAS冲突须先提交aborted空计划；未知COMMIT仅经主库确认本次AttemptID后才能返回确定冲突。GET按Artifact→Draft锁内读取一致快照，摘要列表不包含内容。纯JSON没有外部绑定，直接在本地事务提交最终决定，不虚构文件准备流程。指定修订发布不改草稿，来源编辑/删除不影响成功回执。完整双库、两独立进程CAS、提交结果不明注入、HTTP/SDK、race/vet/build及OpenAPI检查通过。此前T01–T04段落为历史切片，本节为当前实施边界；T06及后续未开始，非空files仍明确拒绝。详见T05交付记录。
