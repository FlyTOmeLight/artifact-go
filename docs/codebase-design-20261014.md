# artifact-go 代码结构设计

初稿日期：2026-10-14；本次文件职责补全：2026-10-15。**当前状态：T01–T11 已实现并通过本地验收，T12 本地及真实 JuiceFS 验收通过；真实身份与生产接入尚未验收。下文保留分阶段设计与历史实施记录，当前结果以 [T12 验收](t12-acceptance-20261015.md)和[当前源码](../service/service.go)为准。**

依据[技术设计](version-service-technical-design-20261013.md)、[领域术语](../CONTEXT.md)和当前[server-go 项目布局](/Users/light/Code/server-go/docs/go-project-layout.md)。业务语义沿用技术设计，代码职责根据当前需求重新划分。当前已实现六个 artifact app、独立 storage、引用交接、后台恢复、Go SDK 和 27 项公开操作。完整状态与分项证据见[实施计划](implementation-plan-20261015.md#当前实施状态)。下文目录和签名包含设计示意，实际装配与导出符号以当前源码为准。

## 六个 artifact app，与独立职责的 storage 模块

artifact、version、draft、alias、retention、resolve 六个 app 管集合与版本语义，内部 backgroundtask 执行保留和版本删除交接。`internal/storage` 独立拥有文件准备、授权、确认、绑定、物理回收及租户 bucket 创建；首期即采用独立事务的引用交接协议：先持久 Bind，再提交 artifact 业务及释放待办，最后后台 Release。同进程部署也不跨域传 tx，拆服务保持协议不变。

artifact 拥有十一张表，storage 拥有三张表，各自一个 table 包。DraftFile与VersionFile记录草稿和版本的逻辑清单，StorageFile记录实际位置和文件生命周期，StorageReference以(ConsumerID,ReferenceID)记录一次持有关系，DraftFile/VersionFile另存内部ReferenceID；两个领域之间不建立数据库外键，也不允许 artifact 直接查询 storage 表。ReferenceChange持久记录准备计划和提交决策；storage先独立BindReferences，版本、清单、成功回执及旧引用释放任务再在artifact本地事务提交。artifact 共21个HTTP操作，storage文件4个、统一grant 1个、租户bucket管理1个，合计27个。统一grant完整归storage模块，公开契约归storagev1。

Module 的 Interface 包括类型、约束、调用顺序、错误和资源责任；Implementation 隐藏执行细节；Seam 是可替换行为的位置，Adapter 提供具体实现。Depth 来自调用方无需重复维护的行为，Locality 来自每项业务规则有明确归属，不由包数量决定。

## 模块 Interface 与规则所有权

| Module | Interface 覆盖的操作 | Implementation 的所有权 |
| --- | --- | --- |
| artifact | 创建、列出、读取及归档集合 | 集合命名、类型、作用域、编号模式和归档状态 |
| version | 发布、列出、读取、更新注释及保护、删除版本 | 本地发布事务、编号、幂等、版本清单与可见性；编排 alias、storage BindReferences及持久释放待办 和保留，不重复实现它们的规则 |
| draft | 创建、列出、读取、更新和删除多份带文件草稿 | 草稿内容与清单可见性、独立DraftID、可编辑JSON和文件清单、统一Revision及复制版本快照与文件引用；不拥有版本发布事务 |
| alias | 查询、条件创建/移动及删除 alias；锁内共用变更规则 | 名称、Revision、墓碑和 latest 只读语义 |
| retention | 设置版本保留规则、预览淘汰候选；纯保留计算 | 策略修订、参数约束、保护集合和淘汰顺序 |
| resolve | 解析持久 URI | URI归属和类型核验、alias/latest及可见性；不授予文件读取权限 |
| storage | 登记/查询文件、确认、删除未绑定文件、统一授权；可信内部绑定与释放；创建租户bucket | 文件scope、物理位置、生命周期、预设/原生S3 Actions授权、凭证有效期和物理回收；只保存可信ConsumerID、ReferenceID和FileID，不学习artifact、版本或逻辑路径规则 |
| backgroundtask | 内部保留评估、过期准备操作中止、引用释放及版本回收确认 | 持久释放任务、业务任务租约和版本删除进度；不访问对象存储 |

没有 `app/file`，没有上传会话、封存接口或下载会话。发布请求里的 `files:[{path,fileID}]` 是版本逻辑清单；storage登记只准备独立文件，不接收ArtifactID或path。客户端上传成功后确认文件ready，再发布引用。下载同样调用统一grant；storage按可信租户/项目、文件状态及S3 Actions签发限权STS，客户端直接读取对象。未绑定且未过期的ready文件可用于上传确认后的预览，bound文件不受历史准备TTL限制；不需要提供业务来源。

不为每张表建立 app，也不为每个 app 声明同名 Service interface。具体对象负责业务行为；跨域只暴露实际需要的独立提交的引用交接契约，授权由storage自行完成。

## 本轮最终grill结论

已确认的七项决定：租户系统触发bucket创建，artifact-go临时承载；内部HTTP与独立storagev1 SDK；TenantID固定规则定位、无映射表；接入层验证租户系统服务身份；确认仅核对对象存在及实际大小；declaredChecksum可选且未经服务验证；发布指纹保留数字写法差异。这七项决定已实施，对应验收见 T06–T12 记录。

剩余的是接入和能力验证：真实身份解析/租户系统验证器、普通IAM STS与过期/跨桶、JuiceFS对象删除和遗留分片回收。旧严格摘要探针只保留历史事实，后续实施时按新契约更新；不据此假称当前后端已通过验收。具体后端、HTTP路径与规则如下，不再预留配置映射或额外业务表。

## Review 时先看这几项

本次补齐带文件草稿、文件复用与回收规则，以及相应代码结构和文件职责。业务语义以技术设计与用户最新决定为准：发布冻结 JSON 和数据库文件清单，发布后的外部对象修改由客户端负责；每租户一个 bucket，同租户项目共享；客户端编排分片，服务不接收上传流。

需要 review 的主要选择：

1. 保留六个artifact业务app和独立storage模块，统一grant归storage；保留两个内部执行器及分属两域的十四张表；复杂操作按行为拆文件，较小的操作仍放 `service.go`。
2. 部署配置统一放公开 `service/config.go`，删除此前拟设的重复 `internal/config`；组合应用只需要公开类型。
3. 身份接入的公开契约放 `service/identity.go`，具体鉴权实现由接入方提供；内部作用域检查留 `internal/scope`。
4. 文件位置与TenantID派生bucket规则归 `internal/storage/client/location.go`；第一版使用一个配置的 BackendID 与多个租户 bucket，不建立多后端插件或动态注册表。
5. URI 编解码与游标分别有小型纯实现，因为实际被多个 app 共用；不建立通用工具包或逐层转发接口。


`retention` 表示版本保留机制，例如一个集合只保留最近10版或保留30天，同时排除显式Protected和规则保护的alias目标。“集合”就是Artifact，例如一个model集合；“按集合取得策略”具体就是用ArtifactID查询该集合的retention_policy记录，没有配置时使用默认禁用规则。它不等于按租户读一个全局策略。

`cursor` 只负责列表续读标记的编码、解析和scope/filter校验，分页查询、limit和排序仍由具体app/table负责，所以不再叫pagination。

## 完整实施目录

下列树列出正式交付的文件范围；实现按切片逐步建立，不能先生成空文件。全部 Go 测试文件集中在 tests/，准备与运行方式见后面的测试职责表。

~~~text
artifact-go/
    go.mod
    go.sum
    README.md
    .gitignore
    Makefile
    .github/workflows/check.yml
    cmd/server/server.go
    service/
        config.go
        identity.go
        build.go
        service.go
    internal/
        app/
            artifact/
                controller.go
                service.go
            version/
                controller.go
                service.go
                publish.go
                number.go
                fingerprint.go
                references.go
                deletion.go
            draft/
                controller.go
                service.go
                references.go
            alias/
                controller.go
                service.go
                mutation.go
            retention/
                controller.go
                service.go
                planner.go
            resolve/
                controller.go
                service.go
        storagehandoff/
            client.go
            protocol.go
            handler.go
        storage/
            controller.go
            service.go
            bucket.go
            grant.go
            complete.go
            binding.go
            worker.go
            table/
                file.go
                reference.go
                task.go
                schema.go
            client/
                types.go
                location.go
                s3.go
                grant.go
        backgroundtask/
            worker.go
            scan.go
            reclamation.go
            references.go
        table/
            artifact.go
            version.go
            draft.go
            draft_file.go
            draft_create_request.go
            alias.go
            version_file.go
            retention_policy.go
            create_request.go
            reference_change.go
            background_task.go
            schema.go
        scope/scope.go
        reference/uri.go
        cursor/cursor.go
    sdk/go/artifactv1/
        artifact_client.go
        artifact_identity.go
        artifact_types.go
        artifact_errors.go
        artifact_api_artifact.go
        artifact_client_artifact.go
        artifact_api_version.go
        artifact_client_version.go
        artifact_api_draft.go
        artifact_client_draft.go
        artifact_api_alias.go
        artifact_client_alias.go
        artifact_api_retention.go
        artifact_client_retention.go
        artifact_api_resolve.go
        artifact_client_resolve.go
    sdk/go/storagev1/                    # 同一module中的独立存储SDK包
        storage_client.go
        storage_identity.go
        storage_api_file.go
        storage_client_file.go
        storage_api_grant.go
        storage_client_grant.go
        storage_api_bucket.go
        storage_client_bucket.go
    tests/
        unit/
            number_test.go
            planner_test.go
            scope_test.go
            reference_test.go
            cursor_test.go
            location_test.go
        integration/
            service_test.go
            http_test.go
            process_test.go
            artifact_test.go
            version_test.go
            publish_test.go
            deletion_test.go
            draft_test.go
            alias_test.go
            storage_file_test.go
            storage_binding_test.go
            reference_handoff_test.go
            grant_test.go
            retention_test.go
            resolve_test.go
            worker_test.go
            reclamation_test.go
            schema_test.go
            storage_test.go
            tenant_bucket_test.go
        sdk/
            artifact_client_test.go
            artifact_identity_test.go
            artifact_errors_test.go
            artifact_client_artifact_test.go
            artifact_client_version_test.go
            artifact_client_draft_test.go
            artifact_client_alias_test.go
            artifact_client_retention_test.go
            artifact_client_resolve_test.go
            storage_client_bucket_test.go
            storage_client_file_test.go
            storage_client_grant_test.go
            storage_identity_test.go
        fixture/database.go             # 真实 PG/MySQL 的测试准备
        fake/storage.go                 # 模拟对象存储行为及故障
        fake/reference_handoff.go       # 控制真实交接的丢响应、阻塞和乱序
    conf/server/config.toml
    scripts/
        check.py
        generate-openapi.sh
        verify_s3_contract.py           # 已有实际后端探针
    docs/
        openapi.json
        openapi.yaml
    deploy/
        Dockerfile
        compose.yaml
        README.md
        storage/                        # 已有 JuiceFS 本机部署与验证记录
    docs/
    CONTEXT.md
~~~

`.scratch/artifact-v1/drafts/` 是已有任务草稿，不进入运行时。`docs/storage-contract/*.json` 是能力证据，不是服务配置。当前已有文件和上面的拟建 Go 文件必须区分；目前仓库仍没有 `go.mod`、服务实现或 SDK 实现。

只保留一个 Go module，模块路径拟为 `github.com/FlyTOmeLight/artifact-go`。依赖使用已发布的 server-go v0.10.0，无本地 replace。Go/toolchain 版本在建立 go.mod 时与发布框架的最低版本核对。artifact十一张表与storage三张表分属两个table包，各自显式声明TableName和域内关联；DraftFile/VersionFile与StorageFile之间无跨域外键。不为每张表单独建包或通用Repository。

## 根文件、公开装配及部署文件

| 文件 | 负责的行为与交付内容 | 不承担的职责 |
| --- | --- | --- |
| `go.mod`、`go.sum` | 单模块依赖及固定校验；服务和 SDK 共用发布模块 | 不拆独立 SDK module、不使用本地框架替换 |
| `README.md` | 构建、配置、启动、两个SDK包使用、必要检查与身份接入前提；链接三份设计 | 不复制整份业务规范 |
| `.gitignore` | 排除构建输出、日志、临时数据库、私有配置和凭证 | 不忽略生成的 OpenAPI 交付物 |
| `Makefile` | `build`、`fmt`、`test`、`check`、`integration`、`doc` 的统一入口 | 不把本地与 CI 写成两套检查流程 |
| `.github/workflows/check.yml` | 调用同一检查入口，运行双库必要用例、构建与生成文档一致性检查 | 本次不设计自动发布或生产部署流程 |
| `cmd/server/server.go` | 进程信号、app.Run、配置路径和退出码 | 不装业务路由、不自行关闭借用 DB、不编排分片 |
| `service/config.go` | 公开Config和ArtifactDatabase/StorageDatabase/Storage/Version/Worker配置子结构；必需字段、数值关系及启动前校验 | 不重复读取配置、不将内部 app 类型放进公开字段 |
| `service/identity.go` | 公开用户身份解析及可信RequestIdentity；另声明服务交接身份验证依赖，认证ConsumerID及可服务scope，接入结果转换为内部输入 | 不实现 JWT、租户成员或角色权限，不相信裸 Header |
| `service/build.go` | 按artifact-only、storage-only或combined装配实际启用的模块与资源，显式传入各自AutoMigrate开关、身份依赖并交付Runtime；artifact-only不创建storageDB，storage-only不创建artifactDB | 组合应用不重复调用 Build；无真实初始化/关闭行为的 S3 实例不包装成框架 Module |
| `service/service.go` | 在现有文件内提供两模块各自构造及combined组合装配：artifact仅接收artifactDB与可信storage交接客户端；storage仅接收storageDB、S3及consumer身份验证；combined才传两域DB并装配本地交接。各自安装路由、worker与Start/Stop | 不承接发布业务；借用 DB 不由本 Service 关闭 |
| `conf/server/config.toml` | 一份可运行的结构化示例，字段与 Config 相符，声明DB、存储连接根、限制和 worker 预算 | 不保存真实密钥、不启用生产假身份 |
| `scripts/check.py` | 拥有本次检查创建的隔离双库 fixture，调用格式、构建、测试、真实 HTTP/多进程检查，失败也清理 | 不控制用户现有业务数据库或长期 JuiceFS 环境 |
| `scripts/generate-openapi.sh` | 固定生成器版本、Controller/SDK 扫描路径和 `docs/openapi.{json,yaml}` 输出，校验生成退出状态 | 登记/确认请求及200响应生成array/items schema，非200保留错误对象；不手改接口字段、不产生第二份契约来源 |
| `deploy/Dockerfile` | 正式模块构建服务，运行镜像只包含所需二进制与证书；配置由外部提供 | 不把本机 credentials.env 打包入镜像 |
| `deploy/compose.yaml` | 本地服务与选定 DB profile 的启动示例，连接已有 JuiceFS；容器使用实际可达的宿主地址并验证，不能用容器的127.0.0.1访问Mac转发；一个初始化实例分别初始化两域schema | 不重新部署 RustFS，不冒充多节点生产模板 |
| `deploy/README.md` | 初始化顺序、身份接入、配置/Secret 所有者、备份及验证入口；解释已有 storage 部署 | 不负责租户业务 CRUD、自动迁移历史数据 |

公开 Config 不引用 `internal/*` 类型；内部 app 只在其 `service.go` 中声明自己确实需要的配置子集，由公开 service 显式传值。此前的 `internal/config/config.go` 不再建立，避免公开字段泄漏内部类型或维护同一配置的两份定义。

Storage 配置包括固定 BackendID、S3/STS Endpoint、Region、path-style、服务凭证引用、文件准备TTL、上传/读授权期限、必填正数MaxFileBytes及storage worker预算；bucket命名规则固定在location.go。Version 配置包括MaxPayloadBytes（默认256 KiB）、MaxAnnotationsBytes（默认64 KiB）与MaxFilesPerVersion，草稿和版本共用；HTTP配置包括MaxRequestBodyBytes（默认8 MiB），覆盖全部JSON入口；Version配置另含正数引用准备执行期限，各副本一致；该期限用于中止未提交attempt，不是storage引用TTL。Worker配置包括artifact后台并发、扫描周期、LeaseDuration、RenewInterval、退出预算。公开ArtifactDatabase/StorageDatabase分别配置连接与连接池，均复用框架驱动配置；按启用模块只要求自己的DB配置，combined才要求两域配置。artifact-only另提供可信storage交接客户端连接/身份配置；两域分别显式控制AutoMigrate，不强制共库。配置校验检查正值、租约续期关系、支持的后端及命名规则有效性，不把配置值当作存储容量预留。

## 每个业务文件的职责

以下所有 `controller.go` 只负责该 app 的路由、请求默认值和绑定、Swagger 注释与 HTTP 输出；普通错误复用框架处理，VersionDeleted / DraftDeleted 的410扩展分别由 version/controller.go 和 draft/controller.go 显式输出。controller 不拥有事务，不调用其他 app 的 Controller/Service 编排发布，不序列化 GORM 关联。请求响应类型直接使用对应 SDK 契约，模型转响应的具体赋值留在相关业务文件，不机械建立 mapper.go。

| 文件 | 业务行为、输入输出与规则所有权 |
| --- | --- |
| `app/artifact/service.go` | Create/List/Get/Archive：名称、Type、精确 scope、初始编号模式、作用域唯一性及归档；返回集合响应；使用参考 URI 和游标纯函数 |
| `app/version/service.go` | Service 构造及 List/Get/UpdateAnnotations/UpdateProtection/Delete：版本可见性、Revision、保护和管理信息；显式删除调用本包 deletion，不执行存储 Delete |
| `app/version/publish.go` | 唯一的 CreateVersion 编排：成功重放、来源快照、持久准备与BindReferences、最终本地事务重验来源/attempt、编号、alias、Plan、逻辑淘汰及成功回执；交接调用在业务事务外，不访问S3 |
| `app/version/references.go` | 创建版本的新增引用计划、ReferenceChange准备/中止/提交条件、原准备ExpiresAt和BindReferences分批调用；失败同本地事务交付不可变补偿任务，不代替draft编排 |
| `app/version/number.go` | 整数与正式三段 SemVer 的校验及递增，计算 Sequence/Number 与上限错误；无 DB/HTTP；集合创建和发布共用该纯函数，不复制两套编号规则 |
| `app/version/fingerprint.go` | 发布请求的业务默认值归一、确定性编码与指纹；不包含 requestKey 自身、请求 ID 或临时凭证；不将版本后来修改的管理字段当作初次请求变化 |
| `app/version/deletion.go` | 调用方已持锁/同 tx 时执行 MarkDeleting 与 FinishDeletion：版本保护、引用阻止删除及可见性规则、完整ReferenceID释放载荷、版本状态、业务任务写入及最终内容清除；自动淘汰允许解除引用时调用 alias 共用规则维护墓碑和 Revision；不建事务、不启动 worker、不删存储对象 |
| `app/draft/service.go` | CreateDraft/ListDrafts/GetDraft/UpdateDraft/DeleteDraft：可信scope、创建/编辑的active及归档检查、历史scope下的条件删除、DraftCreateRequest持久幂等、多草稿ID及摘要列表、Artifact → Draft锁内一致快照、完整JSON与文件清单替换、从同集合ready版本复制JSON及文件引用、条件Revision更新和删除；编排references.go，最终本地事务按Revision提交JSON/清单/保存回执和移除引用任务；发布由version读取一致快照后重验，不调用draft HTTP Service |
| `app/draft/references.go` | 创建/保存草稿的ReferenceChange准备与独立BindReferences、保留引用差集、失败补偿和成功appliedRevision回执；只改JSON或路径沿用原ReferenceID，无新增引用省略查询及Bind |
| `app/alias/service.go` | Put/Delete/Get：可信身份和作用域、直接操作的 Artifact 锁与事务、普通引用及 latest 查询；写操作调用本包 mutation，不重复实现 Revision、墓碑或目标版本校验 |
| `app/alias/mutation.go` | 供直接操作、发布及自动淘汰共用的锁内变更：名称及 latest 禁写、Revision 检查、墓碑重建、修订递增；绑定时核对目标版本属于同一 Artifact 且 ready；发布前检查条件修订，发布写入版本后执行绑定；自动解除引用只处理指定集合和淘汰目标的当前引用；使用调用方同一 tx，不自建事务、不读取 HTTP 身份、不决定版本能否淘汰 |
| `app/retention/service.go` | UpdateRetention/PreviewRetention：字段存在语义、规则约束、Revision、作用域；预览构造Plan输入且不写库；更新策略安排后续评估，不自己运行扫描 |
| `app/retention/planner.go` | 无 I/O 的 PlanInput/PlanResult 与候选计算：Protected、alias、最低保留、数量/年龄、排序及 reject/deleteOldest；预览、发布和 backgroundtask 共用 |
| `storage/controller.go` | 文件四个用户路由与POST /v1/storage/grants统一授权路由经IdentityResolver；登记/确认直接绑定并输出数组，区分整请求错误与逐项结果；bucket管理路由单独经TenantServiceVerifier；绑定/释放只接受可信内部调用；controller不拥有事务 |
| `storage/service.go` | 构造及RegisterFiles/GetFile/DeleteFile：可信精确scope、声明字段、登记requestKey幂等、固定位置、文件状态与TTL；新登记要求active，成功登记重试可在可信历史scope确认原记录，不延长期限；登记不接收逻辑路径；只允许显式删除未绑定文件；绑定后只允许可信Release |
| `storage/bucket.go` | CreateTenantBucket：固定名称、后端存在/创建和同租户重试确认；普通文件登记不自动建桶 |
| `storage/grant.go` | CreateGrant：展开preset或校验原生S3 actions，拥有短事务并按FileID升序锁全部StorageFile；检查可信精确scope、动作所需状态与期限，含上传动作时持久化全部LastGrantExpiresAt；提交后调用Adapter.CreateGrant签发整批一份STS，上传动作签发后重查全部归属有效性、状态和期限再返回。准备与签发是本模块私有步骤，不公开跨模块拆分接口，不读取artifact表、不存密钥、不立即撤销旧凭证 |
| `storage/complete.go` | CompleteFiles：逐项事务外Stat，短事务重新锁文件并核对scope、uploading、期限；仅核对存在与大小，成功ready；失败保留uploading可重试，ready或bound重复确认返回已确认记录，不改变引用，不删对象 |
| `storage/binding.go` | BindReferences/ReleaseReferences/GetReferenceStatuses：自持storage事务，校验服务身份ConsumerID与精确scope，按FileID升序锁；一次ReferenceID生命周期、先Release墓碑拒绝迟到Bind、稳定释放结果及最后引用deleting+StorageTask；不接收调用方tx或查询artifact表 |
| `storagehandoff/handler.go` | 内部HTTP查询/Bind/Release/状态传输、服务身份及精确scope验证；依赖BindingService接口而不访问数据表，不进入用户SDK |
| `internal/storagehandoff/client.go` | artifact使用的HTTP/JSON交接客户端；限定超时与重放、转换结果未知错误，不访问两域数据库或S3 |
| `internal/storagehandoff/protocol.go` | 内部请求/响应与稳定错误契约，仅传scope、FileID、ReferenceID及确认元数据，不携带业务owner |
| `storage/worker.go` | storage自己的过期扫描、任务领取续租及物理Delete：停止新授权后等待LastGrantExpiresAt，事务外删对象，锁内核对租约后deleted；受控周期复查deleted墓碑处理迟到写入，不复活文件 |
| `app/resolve/service.go` | Resolve：解析 URI 后核对 scope/type，定位可见集合/版本/alias/latest/文件并返回引用及元数据；不打开文件流，不修改引用或上传状态 |
| `backgroundtask/worker.go` | Worker依赖构造及Run入口、领取、按 Kind 分派、有界并发、租约续期、失败重排、取消和退出等待；backgroundtask 的至少一次执行与租约所有权在此；不计算保留规则 |
| `backgroundtask/scan.go` | 扫描artifact保留评估、未完成删除及到期preparing ReferenceChange；短事务重新判定，调用Plan/MarkDeleting或中止并持久交付补偿；不接管业务提交，不扫描storage表 |
| `backgroundtask/references.go` | 按不可变任务载荷及notBefore发送ReleaseReferences，重复/乱序可恢复；到期preparing只能在本地事务竞争中止并登记全部计划引用，不替用户提交业务，不读取已删除清单重建任务 |
| `backgroundtask/reclamation.go` | 通过GetReferenceStatuses查询删除交接中全部ReferenceID的释放状态；全部完成后，在同一事务核对LeaseToken、调用FinishDeletion并将业务任务done；不执行S3调用，也不读取storage表 |

较小操作继续放 service.go；不预建 list.go/read.go、每个方法一个文件或每个 app 同名 Service interface。跨 app 共用的编号、Plan、删除规则，以及独立的文件授权流程，按各自行为拆文件。

## 按表文件的职责

所有 Table 函数接收标准 context 和本次 `*db.DB`/tx，做条件查询和更新，不自开/提交业务事务，不读取用户身份，不计算指纹、编号或保留候选规则。

| 文件 | 模型与实际表名 | 查询和更新的具体范围 |
| --- | --- | --- |
| `table/artifact.go` | Artifact / artifacts | scope+Type 命名空间查询，分页、归属读取、行锁、归档、编号计数器与下次保留时间更新 |
| `table/version.go` | Version / versions | Sequence/Number 查找、可见列表、保留计算所需事实、快照写入、注释/保护的条件更新、版本状态及清除内容 |
| `table/draft.go` | Draft / drafts | DraftID主键、ArtifactID普通索引及外键；集合摘要列表、锁内读取、Revision条件替换及硬删除；不分配编号或发布 |
| `table/draft_file.go` | DraftFile / draft_files | DraftID+LogicalPath及DraftID+FileID分别唯一；持久保存草稿文件清单、内部ReferenceID和确认元数据，随JSON同Revision整体替换；不跨域外键到StorageFile |
| `table/draft_create_request.go` | DraftCreateRequest / draft_create_requests | (ArtifactID,ActorID,RequestKey)唯一；保存RequestHash、DraftID、CreatedAt及成功创建结果身份；ArtifactID有域内外键，DraftID无外键，草稿硬删除后保留记录；不缓存内容或创建时Revision |
| `table/alias.go` | Alias / aliases | 唯一名称查询含墓碑、引用事实、Revision 条件更新、墓碑写入；latest 是业务计算，不写此表 |
| `table/version_file.go` | VersionFile / version_files | VersionID+LogicalPath唯一、VersionID+FileID唯一；保存内部ReferenceID及文件Size/ContentType/DeclaredChecksum快照，不保存Bucket/ObjectKey，不跨域外键到StorageFile |
| `table/retention_policy.go` | RetentionPolicy / retention_policies | 用 ArtifactID 查询该集合的版本保留规则、Revision 条件写入、初始化默认策略事实；不运行 Plan |
| `table/create_request.go` | CreateRequest / create_requests | 按 ArtifactID/ActorID/requestKey 查成功记录、插入指纹及 VersionID；不缓存 HTTP 响应，不自行编码请求 |
| `table/reference_change.go` | ReferenceChange / reference_changes | (ArtifactID,ActorID,OperationKind,RequestKey)唯一；固定指纹、scope、当前AttemptID、preparing/committed/aborted、截止时间、新引用计划及原始ExpiresAt、appliedRevision等成功回执；只允许当前attempt的条件提交/中止，不保存大JSON重放副本 |
| `table/background_task.go` | BackgroundTask / background_tasks | Kind/TargetID唯一记录；引用补偿TargetID为AttemptID，删除交接使用独立ID，保存完整不可变ReferenceID/FileID/scope/notBefore载荷，禁止可复用逻辑请求键覆盖旧任务；SKIP LOCKED 领取、DB 时间、LeaseToken 条件续租/完成/失败/重排 SQL；不判断业务目标是否合法 |
| `table/schema.go` | 显式列出artifact十一种模型 | 新MVP的AutoMigrate和双库选项；不管理storage模型，无历史升级链 |
| `storage/table/file.go` | StorageFile / storage_files | 精确scope、物理位置、声明、expiresAt、LastGrantExpiresAt、状态；持有关系归StorageReference；保存RequestKey/RequestHash/CreatedBy/ScopeKey及登记唯一键，按ID批量排序锁和条件状态更新 |
| `storage/table/reference.go` | StorageReference / storage_references | ConsumerID+ReferenceID唯一，固定FileID与scope，存active/released及ReleaseRequiresDeletion；同StorageFile锁内创建/释放及先Release墓碑，released不可恢复；文件外键限storage域内，首期即使FileID deleted也保留墓碑且无TTL，不能丢失可查询收尾回执 |
| `storage/table/task.go` | StorageTask / storage_tasks | 文件回收任务、DB时间、SKIP LOCKED、租约token及有界重试；不判断版本保留 |
| `storage/table/schema.go` | 显式列出storage三种模型 | 自己的建表和索引；支持独立DB连接，启用模块的schema须成功，不建立跨域外键 |

表常量与持久化字段归 table；table 不导入 SDK。业务把数据库事实转换成 SDK 枚举与响应。真实数据库驱动错误的约束映射放在执行操作的 Service；通用可重试 DB 错误仍由框架负责。

## 共享文件、存储文件与身份接入

| 文件 | 职责及调用方 |
| --- | --- |
| `scope/scope.go` | 内部可信身份类型、context 读写、精确租户/项目和子资源归属检查、归属 active/deleted 状态检查；Controller middleware 与各 app 共用；ProjectID 空不扩大范围 |
| `reference/uri.go` | artifact:// 集合、版本、alias、文件 URI 的类型化生成/解析与逐段编码、逻辑路径校验；artifact/version/alias 生成，resolve 解析；不执行 DB 查询，不生成 S3 key |
| `cursor/cursor.go` | 集合 ID/版本 Sequence/草稿 ID 三类游标的编码解码与作用域、筛选一致性检查；不读 DB、不决定排序、不承诺跨页快照；artifact/version/draft 共用 |
| `storage/client/types.go` | 内部ObjectInfo（仅Size）、Grant与Adapter 所需私有配置；不暴露第三方类型、不重复 SDK HTTP struct 的 binding 标签 |
| `storage/client/location.go` | TenantID按固定规则派生bucket的纯定位逻辑、独占FileID的实际ObjectKey生成（files/<fileID>，可带部署固定前缀，不含tenant/project业务层级）、BackendID一致性核对；注册时固定完整位置；后续使用 StorageFile 已保存的 bucket/key，不能重新定位历史文件 |
| `storage/client/s3.go` | 构造并持有SDK客户端，EnsureBucket存在检查/创建及Stat/Delete、对象存在与实际大小转换、错误链和 context；Delete 不存在即成功；不查询或计算可信摘要，不以ETag声称已验证 |
| `storage/client/grant.go` | CreateGrant的协议实现：展开后的原生S3 Actions、多个exact-key的普通IAM STS policy、期限和连接参数；策略超限明确拒绝，不扩大资源；不查看业务表 |
| `tests/fixture/database.go` | 仅供测试引用的同一批 PG/MySQL 连接与隔离 schema、fixture 清理；可启动隔离子进程的测试取得独立连接；不实现内存数据库替代锁语义 |
| `tests/fake/storage.go` | 仅供测试引用的受控 Adapter：可安排Stat/授权/删除失败、阻塞与迟到对象，以验证 app 行为；不建立第二个生产 local 后端 |

artifact业务错误码与对外错误结构放 `sdk/go/artifactv1/artifact_errors.go`；普通HTTP错误复用框架 serrors，VersionDeleted 和 DraftDeleted 的特殊响应分别在 version/controller.go 与 draft/controller.go 输出，不设独立 httperror 包；各 Service 直接使用框架错误类型及这些固定错误码，不再建立 internal/errors 的重复定义。涉及删除版本或已删除创建/保存结果草稿的错误携带必要身份，PUT成功重放的DraftDeleted另带appliedRevision，由对应controller输出 HTTP 410，SDK 保留框架错误链。storage普通错误复用框架；确需稳定的存储文件错误码归storage_api_file.go，授权错误码归storage_api_grant.go，不导入artifactv1错误包。外部网络错误的protocol→领域错误转换留在storage，DB唯一冲突到业务错误留在相应app。

身份接入提案：公开 `service.IdentityResolver` 接收本次 `*http.Request`，返回 `service.RequestIdentity` 或错误；RequestIdentity 只包含 UserID、TenantID、可选 ProjectID 和当前归属 active/deleted 状态，不带角色/Permissions。service 构造时必须注入 resolver，在 HTTP 请求进入业务前执行，再由内部 scope 保存经过验证的结果。service 包负责公开类型到内部类型的转换，scope 不反向导入 service，避免循环 import。SDK 的 RequestIdentity 仍只是 BearerToken 与作用域选择输入，不可直接转换为可信服务端身份。

归属状态表示租户及可选项目整体是否仍有效，不表示操作权限。历史作用域可返回 deleted；读取与已成功发布请求的幂等确认按原设计允许，新建集合、上传及其续发与新发布检查 active。后台根据已保存的目标执行，不需要操作者 resolver。独立 Build 的真实 resolver 提供方式仍由接入环境确定；缺失时不得启用把 Header 当身份的生产实现，测试夹具可显式注入受控 resolver。

第一版固定一个S3 BackendID（例如s3-main），TenantID按固定规则派生一个bucket，同租户项目共用。文件登记固定实际位置，历史对象不因配置改变被重新解释。租户系统触发bucket创建，尚未创建bucket只阻止文件操作，不阻止无文件JSON版本。storage拥有独立模块、表、SDK与事务。首期即使用独立提交协议；首期采用HTTP/JSON交接，以后拆服务只调整端点与装配并验收服务身份、网络与运维，不重写成功条件。

## SDK 每个文件的职责与 27 个操作归属

| 文件 | 负责的公开内容或调用 |
| --- | --- |
| `artifact_client.go` | ClientConfig、NewClient、持有框架 sdk.Client、每次调用的传输准备及 Close；不保存用户身份；默认 Retry=0 |
| `artifact_identity.go` | WithRequestIdentity 与本次 context 的 Bearer/租户/项目/RequestID 到 Header 的转换；不验证身份、不修改共享默认 Header |
| `artifact_types.go` | 公共具名枚举及真正共用的路径/分页结构；VersionIdentity、FileChecksum 等领域内容类型留对应 api 文件 |
| `artifact_errors.go` | 错误码、ErrorResponse、VersionDeletedError、DraftDeletedError 与 Unwrap、410 扩展解码及413限制错误码；保留框架错误、传输错误和 context 错误身份 |
| `artifact_api_artifact.go` | Create/List/Get/Archive 请求与集合响应 |
| `artifact_client_artifact.go` | CreateArtifact、ListArtifacts、GetArtifact、ArchiveArtifact：4 个 HTTP 操作 |
| `artifact_api_version.go` | 发布、列表、读取、注释、保护、删除请求及版本/文件清单响应，VersionIdentity和声明摘要快照类型 |
| `artifact_client_version.go` | CreateVersion、ListVersions、GetVersion、UpdateAnnotations、UpdateProtection、DeleteVersion：6个HTTP操作 |
| `artifact_api_draft.go` | Create/List/Get/Update/Delete请求、Draft及列表响应、复制JSON与文件清单输入、创建/更新必填requestKey、UpdateDraftResponse{appliedRevision,draft}（draft为完整DraftResponse）、Create/Get返回DraftResponse，List返回摘要分页、Delete为空响应、统一Revision条件语义及无JSON/files的DraftSummary |
| `artifact_client_draft.go` | CreateDraft、ListDrafts、GetDraft、UpdateDraft、DeleteDraft：5个HTTP操作 |
| `artifact_api_alias.go` | 条件 alias 请求、墓碑与 latest 响应 |
| `artifact_client_alias.go` | PutAlias、DeleteAlias、GetAlias：3 个 HTTP 操作 |
| `artifact_api_retention.go` | 完整策略替换、显式 null/字段存在语义、预览请求与响应 |
| `artifact_client_retention.go` | UpdateRetention、PreviewRetention：2 个 HTTP 操作 |
| `artifact_api_resolve.go` | Resolve 请求及解析结果 |
| `artifact_client_resolve.go` | Resolve：1 个 HTTP 操作；只取得引用与元数据 |
| `storage_client.go` | storagev1的ClientConfig、NewClient和Close；只持有传输配置，默认Retry=0 |
| `storage_identity.go` | storage用户操作从本次context生成Bearer及scope Header；不共享用户Header、不验证身份 |
| `storage_api_file.go` | Register/Get/Complete/Delete契约；Register/Complete请求和200响应为具名切片、顶层数组，无files/fileIDs包装；FileChecksum声明和文件状态；不暴露artifact类型或第三方SDK类型 |
| `storage_client_file.go` | RegisterFiles、GetFile、CompleteFiles、DeleteFile：4个HTTP操作；不调度multipart |
| `storage_api_grant.go` | CreateGrant请求/响应、scope预设或原生actions、fileIDs、可选ttlSeconds、STS凭证及全部文件位置；不包含ArtifactID/VersionID/DraftID或业务来源证明，不导入artifactv1或AWS类型 |
| `storage_client_grant.go` | CreateGrant：1个统一授权HTTP操作；预设与自定义actions原样传输，不调度multipart或代理下载 |
| `storage_api_bucket.go` | CreateTenantBucket请求与TenantBucketResponse |
| `storage_client_bucket.go` | CreateTenantBucket：1个内部管理HTTP操作，使用本次服务鉴权凭证 |

artifact为4+6+5+3+2+1=21项，storage文件4项、grant 1项、bucket管理1项，共27项。公开契约只依赖标准库和框架允许的SDK类型，不依赖Gin/GORM/internal；各 api 文件提供 Controller 绑定与 Swagger 的唯一契约类型来源；对象用 struct，登记/确认批量请求与成功响应用具名切片。对象静态默认值在绑定前应用；登记数组项按批量契约应用逐项默认值；业务默认值由Service处理，客户端不静默改变语义。

SDK网络调用结束后使用sdk.Complete(response, err)，response为本次HTTP的*resty.Response。所有业务接口返回JSON或空响应，统一授权返回一份限权STS、全部文件位置及expiresAt；客户端用自己的S3客户端读写文件。发布及alias条件更新不由泛化配置无差别重试；requestKey调用由调用方显式同键重试。

文件路由：POST /v1/storage/files（顶层登记项数组）；GET和DELETE /v1/storage/files/{fileID}；POST /v1/storage/files/complete（顶层FileID字符串数组）。统一授权为POST /v1/storage/grants，包含fileIDs及scope；所有动作均由storage检查可信精确租户/项目及文件状态，读取允许未过期ready或bound文件；不依赖版本或草稿。旧上传和版本/草稿访问授权路由不保留兼容入口。

其他接口不作机械去包装：grants 有权限与凭证等并列字段；草稿/版本有 JSON 和清单；保存回执有 appliedRevision 与当前 draft；分页有 items/nextCursor；预览及 Resolve 返回多种具名结果。单资源直接返回对象，无统一 data；具名命令字段不改成裸字符串。完整审查见[技术设计](version-service-technical-design-20261013.md)。

## 测试文件的具体范围

全部 Go 测试集中在 tests/，与服务、SDK 同一个 module，没有额外 go.mod。unit 测试可导出的纯行为，integration 测试业务及真实DB/HTTP，sdk 测试公开客户端。测试通过正式 Interface 调用，不为了集中测试而导出私有实现或复制业务规则。私有 fingerprint 的规范化由发布幂等集成用例验证。

| 文件 | 覆盖的可观察行为 |
| --- | --- |
| `tests/unit/number_test.go` | 整数初值/上限与SemVer递增；调用集合和发布共用的导出编号函数 |
| `tests/unit/planner_test.go` | 数量/年龄、显式保护、alias、最低保留、reject/deleteOldest，无I/O |
| `tests/unit/scope_test.go` | 缺失身份、精确tenant/project、active/deleted，不测试外部JWT实现 |
| `tests/unit/reference_test.go` | URI round-trip、逐段编码、非法路径和不改变scope |
| `tests/unit/cursor_test.go` | 集合ID/版本Sequence/草稿ID游标、跨scope/筛选拒绝、畸形输入 |
| `tests/unit/location_test.go` | TenantID派生bucket规则、ObjectKey规范、BackendID和历史位置 |
| `tests/integration/service_test.go` | 公开构造、Start/Stop、部分启动、worker等待、借用连接不被关闭 |
| `tests/integration/http_test.go` | 21项artifact路由、4项storage文件路由、1项grant和1项管理路由、绑定默认值/false/0/null、201/200/202/410/413、JSON请求体流式限额、可信scope、访问授权与客户端直读边界；410特殊输出一起验证 |
| `tests/integration/process_test.go` | 独立app.Run配置/启动/退出，双进程共享各域PG/MySQL的幂等和任务接管；另验证artifact/storage两个服务进程和两个独立数据库的交接 |
| `tests/integration/artifact_test.go` | scope+Type名称唯一、归档、租户项目隔离 |
| `tests/integration/version_test.go` | 可见性、Number筛选/Sequence分页、注释替换、保护Revision及归属 |
| `tests/integration/publish_test.go` | 编号、请求指纹等价与冲突、响应丢失、归档后幂等、alias/保留回滚、先持久引用再业务本地提交、失败补偿、同scope跨草稿/版本/Artifact复用及发布保留草稿引用 |
| `tests/integration/deletion_test.go` | Protected/alias约束、墓碑、JSON删除、文件deleting及410身份 |
| `tests/integration/draft_test.go` | 多草稿创建/列表/条件更新、复制、删除及旧ID拒绝；同Draft双客户端409、不同Draft独立；创建响应丢失同键恢复当前完整快照、创建后编辑/删除/归档/source变化重试、异参409、已删除410不复活；Get与更新/删除交错不混用Revision和清单、首次创建/更新响应为自身提交快照；PUT同键重放的appliedRevision与当前快照分离、删除后410、结果不明与Revision冲突区分；归档及历史scope下CAS删除释放引用；编辑/删除与发布竞争；成功后草稿变化不影响幂等；JSON和files统一CAS、草稿文件持久保留及不参与latest/保留；草稿/版本/复制/发布/注释一致大小边界及超限事务回滚 |
| `tests/integration/alias_test.go` | Revision、墓碑重建、latest只读、跨集合及非ready目标拒绝；与发布绑定共享规则的一致性 |
| `tests/integration/storage_file_test.go` | 登记/查询/确认/删除，登记同键重试/冲突/终态不复活，顶层数组及空/重复/超限整体拒绝、旧包装/null/非法元素拒绝且无副作用、逐项部分成功与重试及等长同序结果；scope、期限、Stat失败及重试、零字节、确认/过期竞争；ready/bound重复确认不改变引用、bound拒绝用户删除 |
| `tests/integration/storage_binding_test.go` | 同scope、ready未到期或bound要求、同ReferenceID重复Bind、released不复活、同ID异参拒绝、ConsumerID隔离、重复Release及先Release墓碑、不同引用共享、跨scope拒绝、排序锁与GC竞争、单批原子与多批部分成功补偿 |
| `tests/integration/reference_handoff_test.go` | intent先于Bind、丢响应/乱序/部分批次、commit/abort竞争、旧attempt拒绝提交、权威库不可读保留引用、同键单活动attempt/最多成功一次、不可变任务载荷、原ExpiresAt补偿窗口；独立服务和独立数据库验收 |
| `tests/integration/grant_test.go` | preset/actions互斥及原生Actions白名单、展开排序、整批一份STS与无部分成功、fileIDs和ttlSeconds校验、upload/download及自定义子集、混合读写拒绝、策略超限；ready未过期及bound读取、uploading/终态拒读、同scope跨业务来源及ready/bound混批、跨scope拒绝；全部StorageFile升序锁下与最后Release、ready过期或显式删除竞争，非最后Release不阻止读取；写授权期限预留及签发后整批重查；ready读期限受ExpiresAt约束、bound忽略旧ExpiresAt、混批取ready最早期限，ready转bound不延长已签授权；历史scope与同scope不同UserID可读；读授权不延缓GC、延迟签发不延长期限 |
| `tests/integration/retention_test.go` | 完整策略替换、null存在语义、Revision、预览无写入与执行重算 |
| `tests/integration/resolve_test.go` | URI/type/scope、alias/latest与可见性；解析只返回元数据 |
| `tests/integration/worker_test.go` | 双库租约、续期/旧token、退避与失败预算、多进程接管、取消等待；不依赖人工HTTP重试 |
| `tests/integration/reclamation_test.go` | storage Delete失败与租约、等旧授权到期、迟到对象复查、bound不因TTL删除；artifact等待该删除交接全部GetReferenceStatuses完成后FinishDeletion和业务任务done同事务；共享文件不等待其他持有关系释放；非最后释放结果不因其他持有关系后来删除而改变 |
| `tests/integration/schema_test.go` | 两域十四模型双库建表及无跨域外键、索引/墓碑唯一、DraftCreateRequest保留结果且无Draft外键、ReferenceChange逻辑唯一键、ReferenceID终态唯一及任务载荷不可变、AutoMigrate开关及失败；不镜像CRUD |
| `tests/integration/storage_test.go` | 通过实际Adapter和受控协议端点核对S3/STS转换、错误/取消、多精确key policy、原生Actions及期限；真实JuiceFS兼容另用已有探针 |
| `tests/sdk/artifact_client_test.go`、`artifact_identity_test.go`、`artifact_errors_test.go` | 构造/关闭、并发身份互不污染、取消及VersionDeleted/DraftDeleted的410错误链、413错误码 |
| `tests/sdk/artifact_client_<app>_test.go` 六组 | 六个artifact SDK分组的method/path/query/body/header/result/error；不复制DB业务用例、不调度multipart |
| `tests/sdk/storage_client_file_test.go`、`storage_identity_test.go` | 文件四路由的传输、状态/幂等响应和每次context身份隔离；登记/确认请求与200响应直接数组，非200标准错误对象；不代替S3协议测试 |
| `tests/sdk/storage_client_grant_test.go` | 统一grant method/path/body/header/result/error、scope两种形式、无业务来源字段和一份STS响应；不能将整体失败误作逐项部分成功 |
| `tests/sdk/storage_client_bucket_test.go` | 服务鉴权凭证、固定请求体及201/200/失败传输契约 |

`tests/fixture/database.go` 只准备真实PG/MySQL连接、隔离schema和清理；`tests/fake/storage.go` 才是模拟实现，可安排读写失败、阻塞与迟到对象。真实数据库fixture和全部测试不能统称fake。生产代码不导入tests下的任何包，集中测试不要求私有函数变成公开API。

## 配置、装配和 HTTP 约定

- cmd/server/server.go 只处理信号、调用 app.Run(ctx, options, service.Build) 和选择退出码，不重复实现配置监听、日志、HTTPHost 和逆序清理。
- service/config.go 声明公开的类型化 Config，app.Run 按字段名一次解析；只有配置 key 与字段名不同才使用 mapstructure 标签。配置校验与公开子类型留在 service/config.go，内部 app 只接收必要配置值，不重复定义整份部署配置。
- service.Build 的形状为 func Build(ctx context.Context, runtime *app.Runtime, cfg *Config) error。它通过 runtime.Initialize 接管符合 server.Module 的 DB 资源；构造无独立生命周期的存储客户端，显式构造业务及公开服务，再由 runtime.AddService 接管。初始化 context 仅用于构造，不用于长期 worker。
- 独立应用 Build 创建并拥有自己的连接；combined组合应用通过公开构造分别注入两域连接和配置（可同实例，事务独立）；独立模块仅注入自身资源和必要的可信交接客户端，不调用子应用 Build 重复初始化。数据库构造分别显式传入AutoMigrate开关。尚未交给 Runtime 的部分资源由构造方清理，已接管资源由框架释放。
- Controller 与 HTTP 业务 Service 使用 *core.Context；数据库和网络调用提取标准 context。对象请求的静态默认值先通过 defaults.Set 预填，随后 binder.Bind(ctx, &req).All().Error() 合并来源并校验。登记/确认直接绑定数组，先做整批结构/长度/重复检查，再按逐项规则应用默认值和校验字段；不让外层绑定错误替代逐项业务结果。错误由 Controller 调用 serrors.AbortWithError 输出；Service 只返回错误。
- SDK 契约文件和调用文件分开；请求具备 json/form/uri、binding 和必要 example/default 标签，枚举用具名字符串与常量，响应不带请求校验标签。false、0、null 的存在语义保持明确，完整策略替换不能直接复用响应字段的裸 bool 作为必填校验。
- 普通 JSON SDK 方法复用框架 sdk.Client 和 sdk.Complete(response, err)，按本次 context 设置身份与取消；文件读写由调用方按授权STS直连存储。410 的版本或草稿身份扩展由本服务 SDK 包装后保留，不丢弃框架错误链。
- 每个 HTTP handler 有完整 Swagger 注释，参数来源、默认值和状态码与 struct/binder 一致；Controller 注释和 SDK 类型是生成来源，make doc 更新 docs/openapi.json 与 docs/openapi.yaml，不手改生成物。

## 依赖方向与跨 app 调用

~~~mermaid
flowchart TD
    Compose[service装配] --> Version[app/version]
    Compose --> Apps[artifact draft alias retention resolve]
    Compose --> Worker[backgroundtask]
    Compose --> Storage[storage]
    Version --> Number[version.number]
    Apps --> Number
    Version --> Planner[retention.Plan]
    Worker --> Planner
    Version --> Mutation[alias.mutation]
    Apps --> Mutation
    Worker --> Deletion[version.deletion]
    Version --> Deletion
    Deletion --> Mutation
    Version --> Table[artifact table十一张表]
    Apps --> Table
    Worker --> Table
    Version --> Storage
    Apps --> Storage
    Worker --> Storage
    Storage --> STable[storage table三张表]
    Storage --> Adapter[storage client S3与STS]
~~~

artifact调用version/number的纯编号函数，version不反向导入artifact。reference、cursor、scope、SDK和框架事务等共用边省略。两个table包不导入app、HTTP或SDK，不自开或提交事务。

关键约束：

- CreateGrant完整归storage；只按可信精确租户/项目、文件状态、Actions与期限授权，不调用artifact app，不检查业务清单或引用来源，也不要求业务证明或授权回调。
- version不调用其他app的HTTP Service。alias共用规则使用artifact本地tx；BindReferences/ReleaseReferences/GetReferenceStatuses不接收tx，artifact不访问storage表。
- storage只认识可信ConsumerID、ReferenceID、FileID和精确scope；业务owner映射由artifact保存，grant不接收引用或业务来源。
- retention.Plan无I/O；version/deletion在本地事务写逻辑状态和完整释放任务；物理Delete归storage worker。
- backgroundtask通过可信交接契约发送释放并查询状态，不导入S3 Adapter；统一使用受认证HTTP/JSON，不持artifact事务等待外部调用。
- 两域各自锁自己的表。ReferenceChange的commit/abort为唯一决策；跨域允许暂时多保留引用，不允许业务成功却无引用。

### alias 共用锁内变更规则

2026-10-15 架构审查已确认：直接 alias 操作、发布时移动 alias、自动淘汰解除 alias 共用完整锁内变更规则，Implementation 放在 `app/alias/mutation.go`。alias 拥有名称、Revision 检查、墓碑重建、修订递增和绑定目标核验；绑定目标必须属于同一 Artifact 且 ready。调用方仍拥有 Artifact 锁和事务，并决定何时允许移动或解除引用。alias 不反向导入 version，使用 table 查询目标事实；不增加通用 Repository、新的 Service interface 或 HTTP 跨 app 编排。

共用 Interface 的调用约束是标准 context、调用方的同一 tx 和锁定的 Artifact 身份；函数不自行开事务或获取另一份连接。直接操作先验证可信 scope 并锁 Artifact；发布和删除复用自己的外层事务。名称校验、条件修订检查与墓碑规则在 mutation 中共用，不因调用路径而改变。

发布在创建版本前通过共用逻辑检查请求中 alias 的名称和 expectedRevision，并计算拟提交后的引用关系供 Plan 使用；此时新版本尚不存在，不提前执行目标 ready 核验或写入 alias。Plan 通过后先插入 ready 新版本，再调用共用绑定逻辑；绑定读取同一 tx 中的新版本，核对目标归属、ready 和当前修订，然后修改引用。任一步失败回滚artifact内版本、编号及alias变更；已Bind引用按持久中止计划补偿。

直接删除 alias 仍要求调用方提交 expectedRevision；自动淘汰的解除引用由 version 的删除规则批准，按锁内当前引用执行，清空 VersionID、写 DeletedAt 并递增 Revision。自动解除只作用于指定 Artifact 中仍指向淘汰目标的引用，不能提供任意 force 或跳过条件检查的通用开关；重复执行不再次递增已解除引用的修订。

验收通过现有 alias、publish、deletion 集成测试的公开操作完成：直接绑定和发布绑定均拒绝错误 Revision、跨集合或非 ready 目标；墓碑重建要求当前修订；自动淘汰解除引用保留墓碑并递增一次修订；后续任一步失败时artifact业务变更整体回滚，已建立的storage引用可靠补偿。使用真实 PostgreSQL/MySQL 验证，不为测试导出额外私有函数。

## 带文件的工作草稿

每个 Artifact 可以有多份 Draft，各自有独立且永不复用的 DraftID 和 Revision。Draft 保存 JSON Payload/Annotations 与完整文件清单，二者受同一 Revision 保护。Draft 不分配 Number/Sequence，不进入 latest、alias 或版本保留；发布后仍可编辑，已发布快照不变。DraftFile 保存逻辑路径、FileID、内部ReferenceID及确认元数据；StorageReference 保存每份草稿或版本对文件的占用。文件内容不复制，同一精确scope内可跨草稿、版本和Artifact复用。

Draft 模型保留 ID、ArtifactID、Payload、Annotations、Revision、CreatedAt、UpdatedAt；初始Revision=1。文件路径在同一草稿内唯一，同一FileID在该草稿内也只能出现一次。JSON与清单一起完整替换，编辑和删除要求正数expectedRevision。删除硬删Draft及DraftFile，同事务记录该草稿全部ReferenceID释放待办；提交即204（释放义务已持久化，storage可稍后释放），不等待物理回收。DraftID不复用，旧页面不能PUT重建。发布用CreateRequest与草稿创建用DraftCreateRequest均不依赖草稿存续；无草稿历史修订、自动过期或自动保留策略，保存成功的文件不会因上传准备TTL到期而删除。

草稿HTTP/SDK有五个操作：CreateDraft（POST /v1/artifacts/{artifactID}/drafts，新建201、成功重试200、原草稿已删除410）、ListDrafts（GET同路径，200）、GetDraft（GET /v1/artifacts/{artifactID}/drafts/{draftID}，200）、UpdateDraft（PUT同资源路径，200）、DeleteDraft（DELETE同资源路径，expectedRevision置query，成功204）。删除或不存在的草稿GET/首次PUT/首次发布返回404；已成功PUT的同键重放返回带身份及appliedRevision的410 DraftDeleted。各操作检查精确scope和Artifact/Draft归属；首次创建和编辑要求active和未归档集合。DeleteDraft允许可信历史scope和已归档集合，仍核验归属及expectedRevision后释放本草稿引用，避免归档使附件永久无法回收。列表按ID升序，limit默认50范围1–200，游标绑定scope及ArtifactID；列表只返回DraftSummary（id、artifactID、revision、createdAt、updatedAt），不含JSON或files、不承诺跨页快照。GetDraft在短事务中按Artifact → Draft加锁，锁内读取JSON、完整files和同一Revision，提交后一次返回；不能仅用READ COMMITTED下的多次普通查询拼装。首次创建和更新响应在写事务锁内组装完整快照，提交成功返回自身提交结果；成功重放返回当前完整一致快照，PUT同时返回原appliedRevision，不能将旧请求内容与新Revision拼装。草稿附件预览使用storagev1.CreateGrant，传fileIDs及scope.preset=download；storage只核验可信精确scope和文件状态后返回STS，不返回文件字节。确认为ready但尚未保存草稿的文件也可在准备期内预览。

CreateDraft必须带1–64字符requestKey，并接受直接payload/annotations及files，或sourceVersionID，两种来源互斥。直接来源files省略等价空清单；sourceVersionID只允许同集合ready版本，锁Artifact后核验并复制JSON和清单，为新草稿新增文件引用，不复制字节。UpdateDraft必须提供1–64字符requestKey、完整JSON、files数组及expectedRevision；files:[]明确清空，省略files拒绝，避免保存JSON时意外清空附件。另存为新草稿提交页面当前JSON与files；旧页面仍须按实际FileID状态校验，不恢复已回收文件。

创建草稿幂等归draft/service.go的私有逻辑，沿用已确定的JSON指纹规范；不新增泛化指纹包。DraftCreateRequest与草稿、清单、ReferenceChange的committed决策同一个artifact事务提交；storage引用提前独立持久绑定，业务失败不记成功记录并可靠补偿；唯一键为(ArtifactID,ActorID,RequestKey)。可信scope检查并锁Artifact后，先查询成功记录，再检查当前active/归档、sourceVersion状态、文件状态或当前配置下内容大小。同键异参409；同参且草稿仍在则加Draft锁并返回当前完整一致快照（200，可能已不是Revision 1）；原草稿已硬删除则410 DraftDeleted，携带draft:{artifactID,id}身份，不返回内容、不创建替代草稿。首次创建201。源版本模式按原始sourceVersionID及创建参数计算指纹，不能重新读取source内容后计算；直接模式按原始JSON和清单计算。创建记录的ArtifactID有外键，DraftID无外键，不能复用带VersionID外键的CreateRequest。

草稿只保存同精确scope内ready且未过期或已有引用的bound文件，不接受uploading。先读一致草稿/来源快照，查询新增文件可信元数据及原始ExpiresAt；ReferenceChange先保存新AttemptID和完整引用计划，再在事务外调用BindReferences。最终artifact事务按Artifact → 可选Draft/来源Version → ReferenceChange取锁，重验当前attempt、preparing、截止时间及Revision/来源可见性，提交JSON、清单、Revision、成功回执和移除引用任务。未变FileID或只改path沿用ReferenceID；移除再添加必须新建ReferenceID。无新增引用省略查询与Bind，但保存回执和移除任务仍可靠提交。

UpdateDraft指纹覆盖DraftID、expectedRevision和完整输入；成功后记录appliedRevision。响应丢失沿用同键同参，成功重放返回原appliedRevision及当前完整一致draft；当前draft.revision可更大。删除后重放返回410 DraftDeleted及原身份/appliedRevision，不复活。RequestInProgress 409表示同请求仍执行，OperationOutcomeUnknown 503表示主库结果不能确认；两者继续原键核对，不能按RevisionConflict提示另一页修改。明确CAS失败须先持久中止，全部计划新增引用补偿不早于原准备ExpiresAt；该窗口不重置，committed引用无TTL。失败attempt以后允许同键同参新attempt，旧任务不能影响新引用。

CreateVersion的draftID与expectedDraftRevision必须成对，与直接payload、annotations、files互斥。准备阶段读取指定修订，新增版本自己的ReferenceID并持久Bind；最终本地事务重验草稿仍存在且Revision匹配，提交版本、成功回执及retention释放待办。保留草稿及原引用，不修改其Revision；同修订不同requestKey可发布不同版本。指纹固定来源标识及发布参数，已成功后编辑/删除来源不影响幂等。失败回滚artifact业务，后台只补偿引用，不代替用户继续发布。

### Web 编辑交互契约

“从已发布版本创建草稿”和“另存为新草稿”显式创建新DraftID；“继续编辑”打开已有DraftID。页面URL须包含ArtifactID和DraftID，刷新、恢复标签页、复制链接或重复打开均读取同一草稿，不能在页面加载时自动创建。客户端首次创建前先保存requestKey和原始请求，网络失败/响应丢失继续使用同键同参，收到200时采用返回的当前完整快照；410时保留本地内容并由用户选择是否新建，不自动复活旧草稿。客户端在已有DraftID上自动保存；服务端不管理标签页、编辑会话或单人占用锁。

同一草稿的每个页面保留与实际JSON和文件清单对应的基线Revision及未保存内容；自动保存按页面串行，保存响应只更新其对应基线，不丢弃保存等待期间的新本地编辑。不能仅把其他页面的新Revision同步过来而保留旧内容。明确RevisionConflict 409时暂停自动保存和发布、保留本地内容，提示“这份草稿已在其他页面修改”，提供查看最新内容或把本地修改另存新草稿；未经用户选择不能覆盖最新内容或自动刷新丢弃本地编辑。404时按草稿已删除/不存在处理，同样保留本地内容并允许另存新草稿。每次保存前保留requestKey和完整请求；网络失败、RequestInProgress或OperationOutcomeUnknown沿用原键同参核对，不能当作Revision冲突或成功。收到重放结果后同时核对appliedRevision与当前完整快照，不能盲目推进基线或直接发布。

发布前若有未保存修改，串行保存当前页面待提交内容，确认返回快照就是本次appliedRevision且与选定内容一致后，再使用DraftID和该Revision调用CreateVersion；若重放已返回更高修订，先处理页面内容差异；已保存且未改动时直接使用其基线修订发布，不额外PUT推进Revision；有冲突或保存失败停止发布，不能发布服务端另一页的当前内容。发布期间的新编辑不改变本次已选修订；其他页面抢先更新导致发布Revision冲突时，保留本地内容并进入冲突处理。发布请求重试沿用原requestKey和已选修订，不自动替换为最新草稿。

测试仍复用已确认HTTP/SDK与真实双库seam：同集合多草稿相互隔离、五操作及摘要游标、创建requestKey在响应丢失/归档/source变化/编辑/删除后的重试及410、可信历史scope下删除、Get与更新/删除交错及写响应一致快照、同Draft两个客户端的Revision竞争、不同Draft独立编辑、复制JSON及附件引用、清单与JSON统一CAS、上传未完成拒绝保存、去掉附件仅释放本草稿引用、删除旧ID不可复用；发布和编辑/删除竞争只得到指定修订完整快照或409/404；成功发布后编辑/删除仍幂等，版本删除后仍410；失败不推进编号或改变alias，草稿不影响latest/保留。用两个独立SDK客户端验证服务端交互，Web接入方验证暂停自动保存、保留本地、另存、刷新不创建和先保存再发布。交互约定不把Web前端实现加入本仓库交付。


## JSON大小与显式文件内容

MaxPayloadBytes默认256 KiB，MaxAnnotationsBytes默认64 KiB，草稿和版本使用同一部署配置。对合法JSON对象执行json.Compact后按UTF-8字节数计量，保留字符串转义及数字词法，去除非字符串空白，不包含外围请求字段；这只是大小计量，不替换既有指纹规范。等于上限允许，超限分别返回HTTP 413 PayloadTooLarge或AnnotationsTooLarge。创建/更新草稿、直接发布、从版本复制草稿、草稿首次发布及版本annotations更新均检查最终要保存的对应字段；已成功幂等请求不因原结果后来编辑或当前限制变更重新检查内容大小。

来源复制的字段大小按数据库读出的JSON表示compact后计量；不承诺持久化往返保留字符串转义或数字文本写法，也不使用float64中转。

全部JSON HTTP入口先按MaxRequestBodyBytes（默认8 MiB）限制实际流式读取，计算包括空白、文件清单与JSON envelope的完整请求体；不能只信Content-Length。超限返回413 RequestBodyTooLarge，不进入业务事务，SDK同样受限。成功幂等重试也受当前请求体限制。三项上限均为正数并在所有副本保持一致；默认值不是客户端随请求可覆盖的参数。配置校验与HTTP接入由service/config.go及统一装配负责，字段大小校验由具体业务入口负责，不能出现草稿可保存却版本绕过同一限制的入口。

大JSON由调用方显式登记为application/json普通文件，payload保留少量元数据和业务自定的入口逻辑path；artifact不为此保留特殊字段名。文件沿用独立且部署必填的MaxFileBytes、直传/确认/引用/回收规则，不自动从payload转存、不自动下载回填、不进行文件JSON语义或真实摘要校验。内容改动登记新FileID，通过草稿Revision CAS替换引用；旧版本继续引用旧FileID。外部字节修改仍由客户端负责，与数据库payload快照的保证不同。文件化不提供增量JSON编辑协议，客户端自行合并连续编辑后保存。

真实HTTP/SDK验证三个限制的恰好等于与超出边界、UTF-8多字节、字符串转义/数字词法、外围空白和无可信Content-Length的请求体、草稿/版本/复制/注释入口一致性及413无部分写入；幂等成功重试不重验当前快照字段大小，仍验证当前body上限。SDK保留相应错误码，不新建公开错误包。

## 文件发布步骤的边界

单个HTTP请求也可以先调用外部存储、再开启短数据库事务；合并接口在技术上可行。是否合并应看独立使用与失败恢复成本，不能把HTTP接口数量当作数据库原子性的要求。

数据库原子性不要求每个动作都独占HTTP接口。当前保留登记、上传授权、确认、草稿保存和发布边界，是因为对象传输与事务不能一起回滚：确认核对外部对象后标记ready；草稿保存用Revision协调JSON和附件；发布原子分配编号、写快照和移动alias。新附件需要准备，复用已有bound附件可直接保存；已保存草稿发布只需CreateVersion。

登记和授权保持独立：登记创建文件身份并承担requestKey幂等；统一grant按当前状态签发scope所需权限，同时服务上传、下载与续发。客户端或SDK可编排调用，但不把授权绑定为登记副作用。合并旧的三种授权路由后共27项HTTP操作，上传、确认、草稿保存和发布的实际步骤仍明确展示。

## 发布事务由 version app 负责

外部调用方和Controller只调用CreateVersion；客户端不调用Bind/Release。version拥有发布编排，storage拥有独立事务。version.Service只依赖实际需要的可信文件交接契约（查询准备元数据、BindReferences）；backgroundtask依赖ReleaseReferences和GetReferenceStatuses。它们封装真实外部失败和状态语义，首期同进程也通过HTTP/JSON客户端调用且不接收tx，不建立泛化Repository或工作流引擎。

CreateVersion顺序：

1. 检查可信scope、静态输入和请求指纹，先确认已有成功CreateRequest，命中则只重放结果。
2. 读取来源一致快照，校验active、内容大小和完整清单；查询新增文件可信元数据及原始ExpiresAt。无新增引用省略storage准备与Bind。
3. artifact短事务在锁内复查成功记录和指纹，串行确定当前attempt。已有有效attempt返回RequestInProgress；到期旧attempt先持久abort并写完整补偿任务。新ReferenceID计划先持久化，再发Bind。
4. 事务外分批BindReferences，所有新增引用确认后才进入业务提交；单批storage原子，多批失败补偿全部计划引用。
5. 最终artifact事务按Artifact → 可选Draft/来源Version → ReferenceChange锁顺序，重验AttemptID、preparing、截止时间、可信归属、来源可见性/Revision；计算编号、alias与retention.Plan。
6. 提交版本/VersionFile及内部ReferenceID、编号、alias、保留决定、CreateRequest、committed及完整旧引用释放任务；任一业务失败只回滚artifact数据，再持久abort并补偿。
7. 成功提交后返回；worker可靠释放旧引用。提交结果不明查主库，主库不可读保留引用，不因超时推断abort。worker仅中止到期preparing并清理，不重放大JSON或提交业务。

version/deletion.go的MarkDeleting/FinishDeletion仍供发布、显式删除和保留评估共用，使用调用方artifact本地tx；MarkDeleting写完整释放任务，不调用storage。Plan仍为纯函数。

Plan计算排除显式保护、配置保护的普通 alias 和最低保留集合，再按旧到新选择候选；reject 超限直接失败，新版本不参与本次淘汰。预览与执行复用该计算，执行在锁内重新求值。失败回滚artifact本地表的变更，不推进编号。最终发布事务不访问storage或对象存储网络；之前成功的Bind不随本地回滚撤销。

## 锁和其他事务的所有者

| 操作 | 所有者 | 事务与锁要求 |
| --- | --- | --- |
| 集合创建、归档 | artifact | 创建依赖唯一键，归档锁Artifact |
| 发布、保护、注释、显式删除 | version | artifact本地锁Artifact → 可选Draft/来源Version → ReferenceChange；只写本域表与释放待办 |
| 草稿创建、编辑、删除 | draft | Artifact → 可选Draft/来源Version → ReferenceChange；来源在Artifact锁下读取/最终重验；JSON、清单、Revision、成功回执及释放任务本地原子 |
| 直接alias操作 | alias | Artifact锁，核对Revision、目标归属及ready |
| 文件登记/删除、绑定/释放 | storage | 自持短事务；所有涉及StorageFile按ID升序锁，再操作Reference；从不取Artifact/Draft锁 |
| 统一文件授权 | storage | 自持短事务，StorageFile升序锁；提交后事务外签发STS |
| 文件确认 | storage | 事务外Stat；短事务重查scope、状态和期限后ready |
| 策略更新与预览 | retention | 更新锁Artifact，预览只读，执行重新计算 |
| 准备操作中止与回收确认 | backgroundtask | 中止只条件更新当前ReferenceChange并同事务写补偿；外部Release/查询不持artifact事务；FinishDeletion和任务done本地同tx |
| 物理回收 | storage worker | 只锁自己的文件/任务；外部Delete在事务外，完成重查租约和状态 |

两域没有跨域锁全序。storage在Bind/Release/GC内部共用StorageFile升序锁；artifact只锁本域数据，不持本域锁等待交接调用。commit和abort竞争同一ReferenceChange，状态/AttemptID不匹配的旧执行者不得提交；任务租约只控制调度，不决定业务结果。

通用事务执行能力已在 server-go/db/transaction.go 发布，业务仓库不建立 internal/dbtx。框架复用 GORM 的事务提交、回滚及 panic 处理，提供 context 取消、隔离级别选项、有界整事务重试与数据库错误分类。业务 app 显式选择 READ COMMITTED、重试预算和事务回调，并决定 本域Artifact → 可选Draft/来源Version → ReferenceChange等锁顺序；框架不认识这些表或业务约束。

重试必须由调用方显式启用，回调只执行可回滚的数据库操作，每次尝试重建中间结果；对象存储调用放在事务外。仅对能确认事务未提交、可安全重试的数据库错误重跑整个事务，唯一键冲突、业务 Revision 冲突及提交结果不明的连接错误不一概重试；后者由业务 requestKey 幂等机制确认结果。框架实现需验证 PostgreSQL/MySQL 的实际错误与回滚语义。

2026-10-15 前置任务已完成并发布为 [server-go v0.10.0](https://github.com/FlyTOmeLight/server-go/releases/tag/v0.10.0)。公开 API 为 db.Transaction(ctx, database, db.TransactionOptions, callback)，默认不重试；启用时显式设置 db.TransactionRetry 的 MaxAttempts、Timeout 和 Delay。框架拒绝嵌套调用，内部函数复用外层 tx；业务实现引用该发布版本。存储前置验证的实际结果见[契约报告](storage-contract/results-20261015.md)，当前仅保留 JuiceFS + Redis。原防覆盖要求已取消；存在与大小核验和限权临时凭证等尚待验收。

Table方法接收标准context和本次tx，已建立事务时不能改用基础连接。Controller不传tx；backgroundtask只将本地tx传给version清理规则；对storage的交接调用在事务外，不传tx。

内部scope包只负责取得可信 UserID/TenantID/ProjectID 并比较精确归属。可信身份的TenantID必须匹配`^[A-Za-z0-9_-]{1,64}$`，保留大小写，不做slug转换；TenantID位于artifact:// URI的authority，因此拒绝`/`、`:`等分隔字符，避免生成无法解析或改变归属层级的URI。UserID及非空ProjectID继续使用opaque标识：最多64个Unicode字符、无首尾空白、不含NUL/CR/LF；ProjectID逐路径段编码，不套用TenantID字符集。缺失ProjectID表示租户直属范围。ProjectID 为空只表示租户直属范围，UserID 不等于资源所有者限制；本版不添加权限字段或租户反调授权 Adapter。读取、重试、URI 解析及后台目标定位都不能扩大作用域。请求字段scope另表示授权的S3 Actions集合，两者不能混为权限来源。

## storage交接和对象存储边界

storage的可信内部操作为BindReferences、ReleaseReferences、GetReferenceStatuses，均自持事务，只接收认证ConsumerID、ReferenceID/FileID集合与可信精确scope，不接收业务ID、调用方tx或普通用户自报consumer。首期统一采用受认证HTTP/JSON，详见[内部线协议](storage-internal-http-contract-20261015.md)，不加入普通storagev1文件SDK或公开用户路由。准备阶段另查询文件可信确认元数据及原始ExpiresAt，必须先持久保存才能发Bind；查询不代替Bind锁内检查。内部类型不泄漏进公开Config或SDK。

service/build.go与service/service.go显式支持独立模块装配：artifact-only进程仅持artifactDB和可信storage交接客户端，不接收storageDB或S3；storage-only进程仅持storageDB、S3及consumer身份验证，不接收artifactDB；combined才分别注入两域DB并通过回环HTTP交接，即使同实例也使用独立事务。HTTP认证把服务身份转换为ConsumerID，所有部署形态都验证服务凭证及消费方scope，不能复用普通用户Header认证consumer。VersionFile/DraftFile保存FileID、ReferenceID、逻辑路径与元数据，不保存物理key。

文件准备统一使用批量接口，单文件传一个元素。POST /v1/storage/files 直接接受 `[{requestKey,size,contentType?,declaredChecksum?}]`；POST /v1/storage/files/complete 直接接受 `["f01","f02"]`。请求不包 files/fileIDs 对象。两者均为1–1000项，空数组、重复requestKey或FileID、超限及无效外层结构整体400；请求入口身份失败401/403。合法登记/确认批量逐项独立执行，HTTP 200直接返回与输入等长且同序的逐项结果数组 `[{...}]`，每项为结果或结构化error；部分成功无需重做成功项。登记仍逐项按requestKey幂等，发布和草稿保存的业务快照仍本地全量原子，storage引用提前独立提交。

登记/确认批量结果本身就是顶层数组，不包 files、items 或 data，按输入顺序逐项返回；登记项带requestKey，确认项带fileID。成功项包含文件字段且error=null；失败项只有关联标识与error{code,message,status}，不含成功字段。status表示对应HTTP状态（如404/409），不等于框架错误编号statusCode。SDK的整体error不代表所有登记/确认项成功，调用方必须检查结果数组中各项 error。顶层 null、非数组、旧包装对象、缺失/非法 JSON、登记元素非对象/null、确认元素非字符串/null 均整体400；数组结构、长度和重复检查先于逐项执行。有效结构中的登记项字段参数错误仍逐项返回。整请求400/401/403/413等失败保持标准错误对象。grant不使用这套逐项结果：一份请求的全部文件共享一组Actions，整体成功或失败。

POST /v1/storage/grants请求包含1–1000个唯一fileIDs、scope及可选正整数ttlSeconds。scope必须二选一：{preset:"upload"}或{preset:"download"}，也可以{actions:[原生S3 Action名称]}替代preset；按字段存在互斥，显式null、空对象、未知字段、同时提供、都不提供、空actions或重复actions均400 InvalidGrantScope。upload展开为s3:PutObject、s3:ListMultipartUploadParts、s3:AbortMultipartUpload；download展开为s3:GetObject。自定义仅接受这四项精确名称，不接受通配符、别名、任意IAM policy或客户端bucket/key；未知/未支持动作整份400 InvalidGrantScope；已识别动作被服务允许规则拒绝为403 GrantScopeDenied，不静默删权限。展开后的actions按字典序返回，全部fileIDs使用相同权限。

含s3:GetObject时只检查可信精确租户/项目与文件状态：ready须按DB时钟确认未过期，bound不受历史准备ExpiresAt限制；uploading、deleting、deleted拒绝读取。允许同scope、同后端的ready和bound文件混批，文件可来自不同草稿、版本或Artifact，无需指定业务来源。上传三个动作中任一项都要求归属active、同精确scope、uploading且准备期未过。包含读和上传动作的请求不能同时满足状态约束，整份409，不降权为子集。登记授权保持独立，续发调用同一CreateGrant重新校验。

成功200返回一份STS credentials{accessKeyID,secretAccessKey,sessionToken}、expiresAt、endpoint、region、forcePathStyle、展开后的actions及按输入顺序的files:[{fileID,bucket,objectKey}]，供客户端直接操作S3。所有文件必须属于同一配置后端；没有逐文件STS或逐项error。任何文件、权限、scope或状态检查失败整份拒绝；策略编码超出后端限制返回400 GrantScopeTooLarge，由调用方拆批，不能扩大为prefix/整bucket或悄悄少授部分文件。文件夹同样按登记的精确key集合授权，逻辑相对路径只进DraftFile/VersionFile。

StorageFile保存RequestKey、RequestHash、CreatedBy和ScopeKey，唯一键为(TenantID,ScopeKey,CreatedBy,RequestKey)。预期size、contentType和可选declaredChecksum规范化比较；同键同参数返回原FileID和当前状态，不延长TTL、不复活终态；同键异参数返回该项409错误。GetFile保留单资源GET；DeleteFile保留单资源DELETE，只接受无有效引用的文件，首次/进行中202，deleted重复200，有引用409；缺失或scope不符不泄漏文件信息。

StorageFile状态为uploading → ready → bound → deleting → deleted；未引用的uploading/ready可显式删除或到期回收。BindReferences接受同精确scope的未过期ready或仍被引用的bound；同(ConsumerID,ReferenceID)与同FileID/scope重复Bind返回原结果，异参拒绝，released永久拒绝。新持有关系使用新ReferenceID，不依据内容去重。FileID/ObjectKey永不复用。

ReleaseReferences只终止本consumer的所列ReferenceID。未存在引用也记录固定FileID/scope的released墓碑，阻止迟到Bind；这种释放不把准备文件直接转deleting。实际active引用释放后仍有其他引用则保持bound，非最后释放结果永久完成；最后引用转deleting并同storage事务写回收任务，需物理Delete回执后完成。GetReferenceStatuses报告active/released及是否等待物理删除。首期released墓碑不自动清理、不设TTL，即使FileID deleted也保留；未来清理须先提供等价可查询回执，本期不实施。

ReferenceChange保存完整计划，失败补偿包含Bind丢响应、未发送或结果不明的全部新增引用；全部项notBefore均不早于可信原准备ExpiresAt，不能按一次查询absent提前补偿，避免迟到Bind后立刻误回收。原期限不重置，文件可能因故障保留更久；成功引用没有TTL。引用计划中止必须与不可变任务同本地事务交接，任务按Kind+AttemptID/独立删除交接ID去重，旧任务不读取新attempt的计划。

草稿删除/清单移除和版本逻辑删除仅在artifact事务中写完整释放任务。DELETE草稿204表示已删除且释放义务持久，不保证storage已经解除引用。JSON版本可本地deleted；带文件版本由worker发送Release并等全部引用状态完成后FinishDeletion。释放交接未到达或其他持有关系存在期间，同scope已知FileID仍可读；无立即撤权承诺。首次Bind与最后Release/GC竞争由storage文件锁决定；deleting/deleted不复活。

storage内部按实际调用定义对象存储Adapter能力：EnsureBucket、CreateGrant、Stat及Delete。它们接受已保存的实际bucket/key；只有storage依赖Adapter，artifact不具备S3字节读取、删除或STS能力。真实实现与受控fake满足同一契约，SDK不暴露AWS类型。

统一授权由storage/grant.go的CreateGrant拥有短事务，只按全部FileID升序持StorageFile锁，检查可信精确scope、Actions所需状态与期限，不取得Artifact/Draft锁、不检查业务清单或指定owner引用。所有输入一次校验；含上传动作时以max(旧值,本次保守期限)更新每个文件LastGrantExpiresAt，与整批校验同事务提交，之后事务外签发一份STS。准备与签发仅是storage内部步骤，不作为跨模块接口。

实际expiresAt不得超过授权检查时刻加请求ttlSeconds（省略取相应配置）、相应上传/读授权配置上限、签发方凭证到期时刻的最小值；含上传动作时再受全部文件ExpiresAt及已持久化期限上界约束；含读取时再受本批授权时为ready的文件ExpiresAt最小值约束，bound文件不受旧ExpiresAt限制。后续ready变bound不延长本次已确定期限。后端最短期限无法满足该上界时拒绝，不能向上取整。签发延迟不能延长期限，返回时已过期则不返回有效grant。上传动作签发后复查身份归属有效性并重新锁定全部文件检查状态与期限，任一归属失效、文件已ready/bound/终态或过期就整份不返回，所有预留期限仍保留，防止响应丢失或并发确认使回收提前。

LastGrantExpiresAt只统计含上传动作的凭证，纯读授权不更新此字段，也不要求回收等待读STS到期。读取授权与最后Release、ready到期或显式Delete竞争，由同一StorageFile锁内状态和DB时钟确定：回收状态先提交或ready已到期则拒绝；授权先通过则允许在锁外完成签发并随后返回，绝对期限不延长，已发凭证不立即撤销。非最后Release使文件继续bound，不阻止新读取。进入deleting后禁止新授权，首轮物理删除等待既有上传授权到期。deleted保留位置墓碑，受控周期清理迟到写入，不恢复可见性；不对bound文件持续核验内容。未完成multipart由客户端Abort或后端生命周期处理。

S3原生权限的粒度保留：s3:PutObject覆盖普通PUT以及分片创建/上传/完成；s3:GetObject覆盖GET、Range和HEAD，不虚构单独HeadObject action。上传预设不包含GetObject；公开允许集合不包含DeleteObject、ListBucket、ListBucketMultipartUploads、ACL/Tag/策略及管理能力。自定义actions不能绕过可信scope、文件状态或资源范围检查；原生动作是请求的权限，不是调用者自行授予的权限。

一份凭证覆盖请求的全部精确key，不限制为单个UploadID或单次写入；有效期内客户端仍可对获准key重新上传、覆盖或中止分片。预期size/MaxFileBytes由登记和确认检查，不是STS强制传输字节上限；确认ready后不能再申请任何上传动作，已发凭证自然到期。实际JuiceFS须验证上传/下载预设、自定义Actions子集、多精确key、跨key/跨桶拒绝、分片动作、GET/HEAD及过期；兼容性不足时拒绝启用该能力，不静默扩大scope。

客户端自行调度单次上传、multipart切分/并发/重试/合并/中止。服务不代理上传或下载流；发布只冻结JSON和数据库清单，外部字节被客户端修改由客户端负责，不新增复制封存、持续摘要核验或防覆盖。确认仅检查存在和实际大小，可选摘要保存为声明，不能以ETag冒充验证。

首期即实施[独立引用交接协议](storage-reference-handoff-design-20261015.md)，至少一组验收使用两个服务进程和两个独立数据库；同库测试不作为拆分边界成立的证据。拆服务保持成功、幂等和恢复条件，另验收身份、传输与部署；不增加业务来源证明、授权回调或分布式事务协调器。


## 两个内部执行器与生命周期

artifact backgroundtask运行保留评估、到期preparing中止、可靠引用释放及版本删除收尾；storage worker运行临时文件过期、物理回收和deleted墓碑迟到写入检查。两者分别拥有BackgroundTask和StorageTask，不提供公共任务创建/查询/重试HTTP或SDK接口。删除响应不暴露内部TaskID；运维通过日志、指标和任务持久记录排障。

两种任务都使用DB时间、SKIP LOCKED、LeaseToken、有界重试和取消等待。storage等待LastGrantExpiresAt、artifact等待storage回收回执都是正常延后调度，不累计失败Attempts或消耗失败重试预算；实际外部调用/任务执行错误才累计失败。超预算failed保留Attempts/LastError并告警，周期扫描不能重置failed绕过预算。人工重排属于交付前须落实的受控运维流程；deleted目标确有迟到对象时可为同一目标重新安排回收，不因此复活文件或版本。

| 文件 | 行为 | 是否调用对象存储 |
| --- | --- | --- |
| `version/deletion.go` | 保护及alias规则、Version deleting、artifact本地tx写完整释放任务；最终内容清除 | 否 |
| `backgroundtask/references.go` | 按不可变任务载荷及notBefore发送ReleaseReferences，重复/乱序可恢复；到期preparing只能在本地事务竞争中止并登记全部计划引用，不替用户提交业务，不读取已删除清单重建任务 | 否 |
| `backgroundtask/reclamation.go` | 发送释放并通过GetReferenceStatuses查询本次删除引用；FinishDeletion与业务任务done同tx | 否 |
| `storage/worker.go` | 等待授权到期、事务外Delete、核对租约后文件deleted及存储任务done | 是 |

artifact模块构造六个业务app及自己的worker，storage模块构造文件/授权路由及自己的worker；combined模式在现有service文件内组合两者，不新增泛化装配引擎。独立模式只注册并启动自身模块，combined的Start启动两者；Stop取消并等待已启动worker退出，部分启动失败也停止已启动者。初始化context不用于长期任务。框架先排空HTTP，再停两个后台执行器，最后释放拥有的DB；借用DB由外部所有者关闭。


## 通过模块 Interface 验证

| 验证位置 | 可观察结果 |
| --- | --- |
| version + 真实PG/MySQL | 编号、幂等、alias、业务清单与释放待办本地原子；Bind先提交，多批部分成功仍不提交业务并可靠补偿，删除后同键重试410 |
| artifact/alias/retention/draft | 命名空间、scope、修订、墓碑、草稿并发与纯Plan规则 |
| storage + 受控Adapter | 不存在/大小不符不ready；零字节有效；确认可重试；授权期限、过期/绑定/删除竞争及交接的ConsumerID/ReferenceID与精确scope限制 |
| 统一grant | 预设/原生actions、整批原子授权、ready未过期或bound读取及跨业务来源混批；一份限权STS直读写，不能以FileID或自定义actions绕过可信scope和文件状态；非最后Release保留读取，最后Release/ready过期或显式删除阻止新授权 |
| 两种worker + 真实DB | 租约接管、有界失败；bound不TTL回收；旧授权到期后删除和迟到写入；本次删除ReferenceID全部释放完成后版本及业务任务一起完成；最后引用须等待物理回执 |
| S3契约 | 上传/下载预设与自定义Actions限权、多精确key、实际期限、bucket隔离、存在/大小核验、Delete幂等 |
| SDK/HTTP与生命周期 | 21项artifact+4项storage文件+1项grant+1项管理路由、存在语义、错误/取消、Swagger、两个worker排空及DB所有权 |

真实PG/MySQL验证锁、唯一键与独立多进程，不以DB fake或单进程race代替。纯Plan直接验证输入输出，跨域状态通过正式行为验证，不新增测试专用公开接口或通用Repository。


## 实施顺序与待接入事实

1. server-go v0.10.0 事务能力已完成；用户 review 本文后，业务侧建立公开配置/身份接入、两域十四张表模型、各自schema、scope，先打通 artifact 与 version 的无文件发布。
2. 实现 alias、retention.Plan 与 version/deletion.go，验证发布及显式删除在artifact本地的原子性。
3. 完成storage文件准备、绑定/释放、统一grant与S3 Adapter；version/draft持久准备、独立Bind后本地提交files清单与成功回执/释放任务，resolve只解析引用。
4. 实现两个内部执行器，验证保留、版本释放交接、物理回收、租约和多副本退出。
5. 按 SDK/Controller/Swagger 契约完成真实 HTTP 与两种数据库验证。

租户/项目删除后禁止新上传、续发上传凭证和新发布的约束保留；可信删除联动输入由接入方落实，成功幂等请求仍可确认历史结果。本次不新增租户授权回调或管理员标志。S3 Versioning、凭证最短期限和部署数值在实际后端接入时落实，不据此建立新业务 app 或插件框架。

## 实施前仍需落实的接入事实

目录和职责可以按本文 review；下列未定事实不能用目录设计冒充已验证能力。

| 事项 | 设计中已确定 | 实施时需取得的事实与影响范围 |
| --- | --- | --- |
| 真实身份与归属状态 | 公开 resolver 接入可信身份和 active/deleted；无角色权限回调 | 谁验证 Bearer、如何确认历史 scope、租户/项目状态从何取得；影响 service/identity 与 Build，不能启用裸 Header 假身份 |
| 租户 bucket 创建与准备 | 一租户一bucket，租户系统触发创建，artifact-go暂时承载创建能力，后续迁出；登记不自动建桶 | 内部HTTP+Go SDK和TenantID固定规则已确认，无映射表；管理身份交由接入层已确认；命名规则已核实当前租户ID并固定；真实鉴权接入、IAM及后端能力仍需实施验证；目前bucket仅测试用途 |
| JuiceFS接入 | 确认对象存在和大小，摘要只保存客户端声明，不读取全文件 | 可信摘要缺口已不阻塞；普通IAM STS/到期/跨桶已有T00新探针证据；自动回收与业务集成仍需验收；旧严格摘要报告仅作历史记录 |
| 幂等输入规范 | 默认值后对完整业务请求做确定性指纹，不含传输元数据 | 已确认数值写法不同即不同参数；空白/字段顺序归一；按同一标准库解码规则处理对象，保留json.Number；不依赖map遍历顺序 |
| 外部 ID 编码 | 服务不改写归属身份；URI/path/objectkey分开处理 | 已核对当前forge-tenant-api：t_或p_加24位小写hex；URI保留ID原值，bucket单独编码；其他接入方需遵循确定的ID契约 |

这些是代码接入或契约兼容的事实，不要求增加新业务模块。文件功能尚未验收不阻止无文件版本切片；真实身份接入未落实时只能使用显式测试夹具开展验证，不声称可直接生产部署。

租户空间归属见[ADR 0002](adr/0002-tenant-bucket-provisioning.md)，本次文件职责与独立事务交接见[ADR 0003](adr/0003-storage-file-ownership.md)。

storagev1的bucket管理文件职责：storage_client.go定义Client配置/构造/Close；storage_api_bucket.go定义创建请求响应；storage_client_bucket.go调用内部创建HTTP接口。tests/integration/tenant_bucket_test.go验证创建结果与重试，tests/sdk/storage_client_bucket_test.go验证管理SDK传输契约。这里只记录已确定的调用形态，管理鉴权复用接入层；创建采用固定名称幂等，命名规则见最终规则。

管理身份接入：service/identity.go声明专用于租户系统调用的服务身份验证依赖，验证逻辑由接入层提供；storage/controller.go的管理路由必须经过它。验证通过只代表可信租户系统，不把普通UserID/TenantID Header视作创建权限，不向storage文件或version添加角色体系，也不由storage自行校验新管理token。未注入该可信验证时管理入口不能启用；测试显式注入受控验证器。

声明摘要决定：客户端负责内容正确性。storage/complete.go与底层Stat只核对存在和大小；SDK文件响应使用declaredChecksum，明确未经服务验证。摘要可选；省略/null等价，响应declaredChecksum:null；提供时仅校验完整声明格式；不把旧严格摘要探针的失败改成通过，也不在本轮设计访谈修改运行脚本。

可选声明摘要已确认：SDK请求使用*FileChecksumRequest（omitempty），响应使用*FileChecksum并保留null；数据库三个声明字段可空且必须全空或全有值。声明只影响登记参数的幂等比较，不保证实际内容相同；无摘要也可ready与发布。

租户空间创建与文件授权/核验同属storage，但消费不同底层能力；bucket.go调用EnsureBucket，grant/complete/worker分别使用授权、Stat及Delete。固定名称的同租户重试不得创建第二个bucket，归属未确认不得返回成功；实际JuiceFS行为在实施时验收。

管理身份装配：service/identity.go另外声明TenantServiceVerifier（接收本次HTTP请求，返回验证错误或成功），由已有接入层提供实现；storage管理路由必须经过此验证器。它不复用普通用户RequestIdentity作为创建权限，也不向业务context加角色。service/service.go负责依赖注入与路由挂接，未提供验证器不能启用管理入口。tests/integration/tenant_bucket_test.go验证租户系统正例、普通用户拒绝以及同租户重试；storagev1 SDK只传递本次服务鉴权凭证，不参与身份验证。

## 可直接实施的租户空间管理规则

当前租户系统源码已核实：TenantID是`t_`加24个小写十六进制字符，ProjectID是`p_`加24个小写十六进制字符（forge-tenant-api的tenant/project Service调用identity.ID）。storage的租户bucket准备按此接入契约验证原始TenantID，不以slug、显示名或大小写转换代替身份；不同bucket命名契约需显式对接。artifact的通用身份边界采用上文TenantID字符集及opaque UserID/ProjectID规则；当前forge-tenant-api生成的ID是这些合法标识的子集，不将bucket命名约束扩大到无文件集合。

固定命名v1：TenantID `t_<24hex>` → Bucket `tenant-t-<24hex>`。不使用摘要、不截断、不随机分配、不维护映射表；在该合法ID集合中名称不重复，符合所选S3命名字符/长度规则。规则常量集中在storage/client/location.go，URI继续保存原TenantID，S3 ObjectKey生成与URI编码分开。名称前缀不作为可热改配置项；历史文件按实际Bucket/ObjectKey读取和回收。

管理入口：POST /v1/storage/tenant-buckets，请求体只包含tenantID。这是租户系统的内部管理路由，必须先通过TenantServiceVerifier，不要求普通用户身份，也不接受任意bucket/backend/path或自动创建租户。service/service.go分别安装业务身份解析与管理服务身份验证，不能在整个/v1父组无差别套普通用户Middleware。

storagev1.CreateTenantBucket返回TenantBucketResponse：TenantID、BackendID、Bucket，不返回管理员密钥。新建返回201，同租户重复/并发创建确认既有空间可访问后返回200和相同位置；不需要独立requestKey或数据库创建记录。底层EnsureBucket负责存在检查与CreateBucket；已存在但不可访问、属于其他存储账号或创建冲突未能确认归属时返回明确失败，不能因名称固定就把所有409当成功。JuiceFS实际兼容性在实施时验证。

无删除租户bucket接口：租户/项目删除不级联物料，版本保留和对象回收仍按原规则执行；不能通过bucket级删除绕过Protected或破坏其他项目文件。storage模块、storagev1 SDK与管理依赖有独立职责；未来拆服务沿用独立绑定/释放协议，仍须验收服务身份、网络和部署。不为尚未开始的迁移增设转发层。

## 发布指纹的最终规则

fingerprint.go对通过校验并应用业务默认值后的请求使用同一确定性编码：JSON对象按键排序，空白及字符串的等价转义不影响结果，数组顺序保留；别名更新列表因名称唯一且顺序不影响结果，按名称排序。files清单按规范逻辑path排序，重复path或fileID拒绝；其余普通JSON数组仍保留顺序。重复alias名仍拒绝。普通JSON对象重复键按所选标准库的解码语义以后值生效，与实际请求处理保持同一路径，不维护另一套解析器。

数字以json.Number保留输入词法形式，不经float64转换。1、1.0、1e0生成不同指纹，同requestKey因此冲突；大整数不因精度丢失变成相同值。requestKey本身、requestID和凭证不入指纹。protected省略/false、业务默认值等按既定规则归一；payload/annotations若显式null或非对象仍按业务契约拒绝，不能靠归一偷偷接受非法请求。

这些规则通过tests/integration/publish_test.go的公开CreateVersion行为验证，不为了测试把fingerprint私有函数导出。此处采用的标准库行为与UseNumber见[encoding/json官方文档](https://pkg.go.dev/encoding/json)；bucket字符与长度要求见[S3官方命名规则](https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html)。

管理契约草图（仅设计，均位于sdk/go/storagev1/storage_api_bucket.go；实际服务调用使用标准context）：

~~~go
type CreateTenantBucketRequest struct {
    TenantID string `json:"tenantID" binding:"required"`
}

type TenantBucketResponse struct {
    TenantID  string `json:"tenantID"`
    BackendID string `json:"backendID"`
    Bucket    string `json:"bucket"`
}

func (c *Client) CreateTenantBucket(ctx context.Context, req *CreateTenantBucketRequest) (*TenantBucketResponse, error)
~~~

service/identity.go的管理验证类型为TenantServiceVerifier func(*http.Request) error，验证器实现由接入方注入。独立管理路由不经过普通用户scope解析，六个artifact app及storage文件路由仍经过IdentityResolver；两者都不存在未验证Header直接转可信身份的路径。配置与公开构造参数不暴露internal类型。

普通IAM/STS多key、权限隔离及到期拒绝已有T00实测证据，探针已更新；当前自动回收、浏览器 CORS 与 failed 任务受控重排已验收；真实身份接入和完整浏览器上传编排仍待验收。它们在对应实施/交付切片验收；第一版不因为尚未做运维工具就新增公开task管理接口，也不把周期扫描当作无限重试。

引用交接故障注入使用tests/fake/reference_handoff.go包裹真实交接实现，仅控制响应丢失、阻塞及乱序，不替代真实数据库状态机；两域独立数据库/进程验收归reference_handoff_test.go。当前目录树是待实施职责，不是已实现证据。

## 已确认的 T00 实施参数

2026-10-15 用户批准 T00 全部建议，并授权原地初始化 Git、仅开始 T01；T02–T12 未启动。参数批准不等于对应业务能力已实现。完整依据见 [T00 review](t00-review-20261015.md)、[配置与受控恢复](t00-configuration-operations-20261015.md)、[内部 HTTP/JSON 契约](storage-internal-http-contract-20261015.md)。

开发环境固定：MaxFileBytes=1073741824（1 GiB），MaxFilesPerVersion=1000（草稿同限），文件准备期24小时；upload/download STS 默认及最大均1小时；attempt固定120秒不续期。内部连接/请求超时1秒/5秒，同步含初次最多3次、退避200/500ms加抖动；内部单批1–1000、请求体8 MiB。每域每副本worker并发2、租约60秒/续租15秒、扫描5秒、退避1–60秒加抖动、每轮最多20次实际失败；每域DB池MaxOpen=10/MaxIdle=5。只等待期限或回执不耗失败预算，失租/崩溃不推测执行失败。生产容量及凭据由部署环境提供。

JuiceFS 当前实测STS最短900秒、session policy上限2048字符。签发前在锁内固定截止D并为写授权预留LastGrantExpiresAt=max(old,D)；调用STS前以floor(D−当前时间−5秒)计算请求期限，不足900秒返回400 GrantTTLTooShort，不能向上取整。返回前校验当前时间<Expiration≤D；失败预留不缩短。多文件按最终policy长度检查，超限400 GrantScopeTooLarge由客户端拆批。只读授权不阻止回收。

内部四操作通过受保护独立HTTP入口：POST /internal/v1/storage/reference-files/query、/internal/v1/storage/references/bind、/internal/v1/storage/references/release、/internal/v1/storage/references/query。请求包含精确scope和数组，成功直接返回同序数组；Bind/Release批内原子。artifact→storage与tenant→storage使用分别配置的Bearer服务凭据，前者映射稳定ConsumerID和精确scope allowlist；跨主机或生产使用TLS，同机回环开发允许HTTP。用户IdentityResolver继续由接入层注入，T01测试可显式fixture，不能将普通Header或现有租户接口403当作可信历史deleted scope。

failed恢复由维护命令执行：CAS仅重排failed且无有效租约的任务，保留不可变载荷/ID/NotBefore、使旧token失效，NextRunAt=max(now,NotBefore)，开启新一轮预算；同事务写操作人、原因及前后状态。任务行增加RunGeneration、TotalAttempts、RecoveryEvents JSON，不增加第15张表或公共task API。实现及双库验证归T04/T07/T09。

正常文件回收等待写凭据期限后，按精确key枚举并Abort未完成分片、Delete对象并复查迟到写入；每小时扫描24小时以上分片仅作补漏，关联文件必须deleting/deleted且写授权已到期，未知key仅报告。该维护扫描使用服务身份，不授予客户端ListBucket权限，不以年龄删除活跃上传。现有trash-days=1保留，S3删除回执不表示SSD空间立即释放。自动回收及真实浏览器链路仍归后续验收。

### T01 审查补充（2026-10-15）

集合归档显式使用READ COMMITTED，整事务最多3次、总预算5秒、重试间隔10毫秒；只重试框架确认已回滚的序列化冲突或死锁，不重试提交结果不明。OpenAPI使用scripts/generate-openapi.sh固定swag v1.8.4生成docs/openapi.json和docs/openapi.yaml，make check必须传播工具及生成器的失败。

MySQL独立装配为空DSNParams填入parseTime=true、loc=UTC等连接参数；显式非UTC参数拒绝启动。组合装配借用DB时，在AutoMigrate前通过DATETIME查询验证UTC时间解码，不修改或关闭调用方连接。DATETIME按UTC写入和读取，不能只在响应中转换时区。

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

T09实现说明：内部服务端传输与client共置于storagehandoff，复用同一线协议并隔离数据库；storage/binding拥有具体事务。service/storage_handoff.go拥有独立监听、TLS、配置映射与退出等待。此布局不改变HTTP边界或引用状态机。T09只接通单文件直接发布，完整草稿/多文件应用仍属T10。

## 实现补齐：运行观测边界（2026-10-15）

`internal/telemetry` 负责有限标签的 Prometheus counter/histogram 和 worker 采集器生命周期，不负责业务判定或数据库查询。各域 worker 自行读取本域任务快照；app 记录 Artifact 锁查询耗时；service 在身份中间件外记录请求结果和可信资源关联。指标读取失败不得修改任务状态或失败预算，停止服务须等待采集中的数据库借用结束。

`service.SetupHTTP` 复用 server-go metrics handler，只在 `HTTPServer.MetricsEnable` 开启时安装 `/metrics`，由运维入口限制访问。默认关闭，不作为公开业务 API/SDK，不引入其他调试或鉴权入口。配置、指标定义和告警操作见 [运行说明](../deploy/artifact/README.md)。
