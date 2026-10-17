# artifact-go

物料集合、版本与文件引用服务。**T01–T11 已完成，T12 本地及真实 JuiceFS 验收已通过**；已新增可选 Forge session 接入与有效租户成员校验；OrbStack 真实登录与文件链路已验收，生产验收另行安排。见 [T12 验收记录](docs/t12-acceptance-20261015.md)。

已实现的操作：

| 方法 | 路径 | 结果 |
| --- | --- | --- |
| POST | `/v1/artifacts` | 创建集合，首次201、有键成功重放200 |
| GET | `/v1/artifacts` | 当前精确作用域内按ID升序分页，200 |
| GET | `/v1/artifacts/{artifactID}` | 读取集合，200 |
| POST | `/v1/artifacts/{artifactID}/archive` | 幂等归档，200；保留名称和编号身份 |
| POST | `/v1/artifacts/{artifactID}/versions` | 发布JSON、完整文件清单或指定草稿修订，首次201、成功重放200 |
| GET | `/v1/artifacts/{artifactID}/versions` | ready版本按Sequence降序分页，可按number筛选 |
| GET | `/v1/artifacts/{artifactID}/versions/{versionID}` | 读取ready版本 |
| PUT | `/v1/artifacts/{artifactID}/aliases/{name}` | 按修订绑定或改绑 |
| GET | `/v1/artifacts/{artifactID}/aliases/{name}` | 读取alias或计算latest |
| DELETE | `/v1/artifacts/{artifactID}/aliases/{name}` | 按修订删除，200返回墓碑 |
| PATCH | `/v1/artifacts/{artifactID}/versions/{versionID}/annotations` | 按修订完整替换注释 |
| PATCH | `/v1/artifacts/{artifactID}/versions/{versionID}/protection` | 按修订更新保护状态 |
| PUT | `/v1/artifacts/{artifactID}/retention-policy` | 完整替换策略，校验修订 |
| POST | `/v1/artifacts/{artifactID}/retention-policy/preview` | 只读计算候选、保护与阻塞原因 |
| DELETE | `/v1/artifacts/{artifactID}/versions/{versionID}` | 逻辑删除；纯JSON直接deleted，带文件等待释放回执 |
| POST | `/v1/artifacts/{artifactID}/drafts` | 新建草稿201，创建重放200 |
| GET | `/v1/artifacts/{artifactID}/drafts` | 草稿摘要按ID升序分页 |
| GET | `/v1/artifacts/{artifactID}/drafts/{draftID}` | 读取一致快照 |
| PUT | `/v1/artifacts/{artifactID}/drafts/{draftID}` | 条件保存，返回appliedRevision及当前草稿 |
| DELETE | `/v1/artifacts/{artifactID}/drafts/{draftID}` | query expectedRevision，成功204 |
| POST | `/v1/storage/tenant-buckets` | 可信租户系统创建固定bucket，首次201、重复200 |
| POST | `/v1/storage/files` | 顶层登记数组，200逐项结果 |
| GET | `/v1/storage/files/{fileID}` | 同精确scope内读取文件状态和元数据 |
| POST | `/v1/storage/files/complete` | 顶层FileID数组，200逐项确认结果 |
| DELETE | `/v1/storage/files/{fileID}` | 未引用文件回收202，已deleted重复200 |
| POST | `/v1/storage/grants` | 按预设或 S3 Actions 为一批精确文件签发临时凭证，200 |



每个集合固定类型、租户/可选项目/可选归属用户和编号规则。归属 userID 与实际操作者 createdBy 分开；空 project/user 分别表示租户级/共享，不是通配符。同一完整 scope+type 内名称规范化为小写且唯一；NULL 归属仍有真实数据库唯一约束。归档不删除集合。retentionPolicy返回当前规则与修订；未配置时返回默认禁用规则、revision=0。

## 构建与检查

需要Go 1.27、C编译工具及Docker（本机可用OrbStack）。正式依赖server-go v0.10.0，无本地replace。

```sh
make build
make test
make check
```

`make test`运行普通测试，没有数据库环境时集成部分会明确跳过。**完整验收使用make check**：启动独占的PostgreSQL 17和MySQL 8.4容器，执行真实HTTP/SDK、并发唯一性、身份隔离、8 MiB请求边界、进程SIGTERM退出、race/vet/build；结束后删除自己的容器，不连接或删除现有应用数据库。

格式工具固定[gorgeous v0.9.0](https://github.com/FlyTOmeLight/go_fmt/tree/v0.9.0)；OpenAPI生成器固定`github.com/swaggo/swag/cmd/swag@v1.8.4`。安装方式由`.github/workflows/check.yml`给出。`make doc`从Controller注解和SDK类型生成`docs/openapi.json/yaml`（Swagger/OpenAPI 2.0），`make check`核对生成物一致性。CI 运行完整检查与模块元数据核对，远端运行记录见 [GitHub Actions](https://github.com/FlyTOmeLight/artifact-go/actions/workflows/check.yml)。

## 可信身份与启动

`service.New(ctx, database, Options{IdentityResolver: resolver, AutoMigrate: ...})`供组合应用使用，数据库由调用方拥有；`service.BuildWithIdentity(resolver)`供`app.Run`独立装配，数据库由框架初始化和关闭。停止服务不会关闭借用的数据库。

MySQL连接必须使用`parseTime=true&loc=UTC`，保证不同机器读取同一DATETIME时得到同一时间。独立装配未设置`DB.DSNParams`时采用`charset=utf8mb4&parseTime=true&loc=UTC&interpolateParams=true`；显式参数不满足要求时拒绝启动。组合应用需自行配置借用连接；`service.New`在迁移前查询一个固定DATETIME验证实际解码，不会更改或关闭调用方的连接。

resolver必须认证本次请求，核对选择的完整资源范围，并返回实际操作者`UserID`、`TenantID`、可选`ProjectID`、可选归属`ResourceUserID`及`OwnershipActive`或`OwnershipDeleted`。`X-Resource-User-ID`只选择资源归属；省略表示共享，显式空值拒绝。SDK通过`RequestIdentity.ResourceUserID`传递。内置Forge接入允许共享范围和已认证用户自己的个人范围。租户ID为1–64位ASCII字母、数字、`_`或`-`，保留大小写；与现有租户系统生成的`t_`加24位十六进制ID兼容。SDK里的scope Header只表达选择，不是身份依据。已删除scope需要接入方仍明确授权历史访问；本服务允许读取、归档及现有版本管理，拒绝新建。

Forge 用户可使用新增 `cmd/forge-server`（`bin/artifact-forge-server`），配置 `[ForgeTenant]` 调用 tenant 的 `/internal/v1/scope/resolve`。该接口要求 active 用户和租户成员，项目只核对归属，不检查 grants；本轮不接受历史 deleted scope。此实现已部署本机 OrbStack，配置和重部署入口见 [Forge 部署](deploy/forge/README.md)。**`cmd/server` 默认 Build 仍在缺少 resolver 时拒绝启动**，自定义接入方使用以下装配位置：

```go
// resolver is the deployment's verified service.IdentityResolver.
err := app.Run(ctx, app.Options{}, service.BuildWithIdentity(resolver))
```

既有测试保留受控 Bearer 的通用装配验证。新增 Forge 进程测试覆盖启动、请求和 SIGTERM 退出；显式设置 `ARTIFACT_TEST_TENANT_REPO` 后，跨仓库测试调用真实 tenant HTTP/session 校验与独立数据库。这些自动化测试的会话在隔离数据库生成；另有[真实 Casdoor 登录与文件联调](docs/orbstack-forge-integration-20261016.md)及[重部署验收](docs/deployment-consolidation-20261016.md)。

配置样例在`conf/server/config.toml`：`ARTIFACT_DATABASE_URL`供PostgreSQL连接，默认监听`127.0.0.1:18080`；MySQL使用框架Host/Port/User/Password/DB字段。AutoMigrate默认关闭，首次受控建表需显式开启；当前创建`artifacts`、`versions`、`create_requests`、`aliases`、`retention_policies`、`background_tasks`、`drafts`、`draft_files`、`draft_create_requests`、`reference_changes`、`version_files`十一张表及归属外键。不要把凭证提交到Git。

## Go SDK

SDK位于`sdk/go/artifactv1`，与根服务同一module。逐请求身份放在context，不修改共享客户端Header；有副作用请求默认不自动重试。逐请求身份也支持 `Session`、`CSRFToken` 和原始 `Origin`，供 Forge session 接入使用；客户端不缓存响应 Cookie，也不自动跟随重定向。服务端身份适配仍需单独装配，传递这些字段本身不代表已完成登录接入。具体契约与已确认范围见[接入方案](docs/tenant-integration-design-20261016.md)。

```go
client, err := artifactv1.NewClient(artifactv1.ClientConfig{Endpoint: endpoint})
if err != nil { return err }
defer client.Close()
ctx = artifactv1.WithRequestIdentity(ctx, artifactv1.RequestIdentity{
    BearerToken: token, TenantID: tenantID,
})
created, err := client.CreateArtifact(ctx, &artifactv1.CreateArtifactRequest{
    Type: "model", Name: "demo", VersionScheme: artifactv1.VersionSchemeInteger,
})
```

## 设计与进度

- [可选用户归属与集合幂等创建契约](docs/service-consumer-design-20261016.md)
- [Issue #2 本地实现与双库验收](docs/user-ownership-acceptance-20261018.md)
- [父Spec #1](https://github.com/FlyTOmeLight/artifact-go/issues/1)
- [实施顺序](docs/implementation-plan-20261015.md)
- [代码结构及职责](docs/codebase-design-20261014.md)
- [技术设计](docs/version-service-technical-design-20261013.md)
- [T00验证](docs/t00-review-20261015.md)
- [T01交付记录](docs/t01-acceptance-20261015.md)

本机 JuiceFS 部署与探针资料保留实际验收依据；当前文件业务链路已接入。T01–T11 与 T12 本地验收记录见实施计划，真实 Forge 身份已完成本机验收，生产接入仍待验收。

集合创建可提供 `requestKey`（至多64字符）；同完整资源范围、实际操作者和键按规范参数重放，首次201、重放200，合法异参409。集合行保存成功事实，归档后重放返回当前状态及retention。无键保持非幂等行为，提交结果不明时不能通过NULL键恢复。旧数据新增user_id为空，保持共享归属；先迁移服务端再启用新客户端，不移除选择Header或请求键降级重试。

## T02 发布边界

CreateVersion必须提供1–64字符requestKey。同集合、同可信用户、同键相同参数重放原结果，异参409；不同用户的请求键独立。整数从1递增，SemVer初值由集合决定，后续省略bump等同patch。归档或归属删除后仍可确认成功请求；墓碑重放返回410及原VersionIdentity，SDK可用errors.As取得VersionDeletedError。列表和普通读取只返回ready版本。

支持直接 payload/annotations JSON 对象（省略默认{}，null拒绝）及完整 files 清单，或指定 draftID/expectedDraftRevision 发布；两种来源互斥。支持发布时设置 aliases 和 protected。文件字节由客户端直接上传到对象存储，artifact 通过进程内文件引用接口绑定 storage 文件后提交版本；两域仍使用各自的数据库事务。

[Version]部署配置控制MaxPayloadBytes（默认262144）、MaxAnnotationsBytes（65536）、MaxRequestBodyBytes（8388608）和MaxFilesPerVersion（1000，草稿与版本共用）。组合构造可传Options.Version，nil使用默认；显式配置必须完整提供四个正值。字段按json.Compact后的UTF-8字节计算，整包按实际流式读取计算。成功重放不重新受字段大小限制，但仍受整包限制；集合创建也使用同一整包限制。

## T03 管理边界

alias首次绑定使用expectedRevision=0；之后改绑、删除或从墓碑恢复必须使用当前修订。latest只读，动态选择最高Sequence的ready版本。发布时alias与版本、保护状态、编号和成功回执在同一事务内提交；重放不会重新移动alias或覆盖后续管理操作。

注释与保护共享VersionRevision，两个页面使用同一修订更新时只有一个成功。显式零修订是过期修订，返回409 RevisionConflict；缺失或负数返回400。归档或历史scope仍允许已授权的版本管理。管理操作提交结果不明返回503 OperationOutcomeUnknown，调用方应读取当前状态确认。

[T03交付与审查记录](docs/t03-acceptance-20261015.md)。后续保留、草稿及文件引用能力均已接入，见下文 T04–T11。

## T04 保留与删除

策略按集合独立设置。完整替换必须显式提供所有规则；maxVersions和maxAgeSeconds允许null，启用时至少设置一个限制。发布超量按reject拒绝或按deleteOldest原子淘汰旧版本；显式Protected总是受保护，protectAliases决定自动淘汰是否解除普通alias。新版本不参与同一次发布淘汰。年龄清理由worker按数据库时间评估，minVersions保护最新的指定数量。

显式删除不受minVersions约束，但Protected或普通alias会阻止删除。成功删除清空payload/annotations，保留ID、编号和成功回执；重复DELETE返回同一墓碑，原发布重放返回410。自动解除alias复用修订规则，墓碑可按当前revision重新绑定。

Service.Start启动本实例worker，Stop取消并等待，借用数据库仍由调用方关闭。[Worker]配置默认并发2、扫描5s、租约60s、续租15s、每轮最多20次确认失败；组合构造用Options.Worker。任务持久化、无全局leader，failed不会被扫描自动重置。正常保护阻塞记录在策略和任务结果，不消耗失败预算。任务没有公开HTTP接口。

维护者先查看任务行ID与Revision，使用私密JSON数据库配置运行以下命令；默认dry-run，仅输出任务摘要及载荷指纹。确认摘要后，在同一参数上添加`--execute`执行CAS重排。配置仅连接当前artifact数据库，不会迁移表；storage域待后续任务实现。

```sh
bin/artifact-maintenance --domain artifact --db-config /secure/artifact-db.json \
  --task-id TASK_ID --expected-revision REVISION --operator OPERATOR \
  --reason REASON --record TICKET
```

重排保留任务身份、载荷、NotBefore及累计次数，同事务追加审计并开启新预算。提交结果不明时命令查询主库审计；仍不能确认则报OperationOutcomeUnknown，不盲目重写。

[T04交付与审查记录](docs/t04-acceptance-20261015.md)记录纯 JSON 切片；当前文件释放与回收由 T09/T10 接入。

## T05 草稿

每个集合可有多份草稿。创建带requestKey，可直接提供JSON或从同集合ready版本复制。保存必须完整提供payload、annotations、files:[]、expectedRevision和requestKey；同一修订并发保存只有一方成功。GET返回完整快照，列表只返回摘要。

同键同参重试创建或保存会确认原结果；保存返回的appliedRevision是原始成功修订，draft.revision可能已经更大，不能把新修订号套到旧内容上。冲突409之前会持久记录aborted；结果不明503继续原键原参确认。删除使用expectedRevision，硬删草稿但保留创建及保存回执，之后成功请求重放返回410及原身份。历史scope和归档集合允许已授权的读取、成功重放与条件删除，不允许首次创建或新保存。

发布草稿使用draftID和expectedDraftRevision，不能同时提交payload、annotations或files。发布不改变草稿；成功后再编辑/删除草稿，原发布重放仍返回原版本或版本墓碑。当前草稿支持完整文件清单，保存、复制、发布及删除的引用语义见 T10。

[T05交付与审查记录](docs/t05-acceptance-20261015.md)记录纯 JSON 草稿验收；带文件草稿另见 [T10 验收](docs/t10-acceptance-20261015.md)。

## T06 租户存储空间

租户系统调用 `POST /v1/storage/tenant-buckets`，请求只含 `tenantID`；固定将 `t_<24小写hex>` 映射为 `tenant-t-<24hex>`。响应只有 `tenantID`、`backendID`、`bucket`。首次创建201，确认已存在且归属当前存储账号、可访问后返回200；不新增数据库表或创建回执，也不提供删除bucket接口。

组合装配传入 `Options.Storage` 和 `Options.TenantServiceVerifier`；独立装配使用 `service.BuildWithVerifiers(userResolver, tenantServiceVerifier)`。Storage为nil时不安装管理入口；启用Storage但缺少可信验证器则启动失败。管理入口不调用普通用户resolver，用户Header不能赋予管理权限。真实租户系统凭证验证仍由接入方实现。

`service.StorageConfig`指定单个BackendID、Endpoint、Region、AccessKeyID、SecretAccessKey、可选SessionToken及UsePathStyle；配置样例在 `conf/server/config.toml` 的注释段。后台账号需要创建bucket、HEAD访问和ListBuckets权限；已存在bucket使用账号所属清单核对归属，不仅依赖HEAD成功。JuiceFS的一个volume对应同一backend存储命名空间，IAM用户不是独立存储账号；这里必须配置命名空间管理凭据，不能用普通用户能列出的bucket推断该用户拥有独立账号。S3调用有超时且不自动重试，客户端遇到未确认结果可用同一tenantID重试。

管理SDK独立位于 `sdk/go/storagev1`，使用 `NewClient`、`WithRequestIdentity` 和 `CreateTenantBucket`；每次调用仅携带服务Bearer凭证与可选RequestID，不发送artifact的租户/项目选择Header。SDK默认不重试副作用请求。

验收和限制见[T06交付记录](docs/t06-acceptance-20261015.md)。本切片不提供文件登记、STS授权或引用交接。

## T07 文件生命周期

storage 与 artifact 由同一个服务进程装配。`Options.StorageDB`、`StorageAutoMigrate`、`StorageFiles` 和 `StorageWorker` 分别控制 storage 数据库、迁移、文件限制与回收任务；`DB` 与 `StorageDB` 保留独立连接和事务。两域数据库都由调用方或 Runtime 拥有。只配置 Storage 而不配置 StorageDB 时仍仅启用 T06 bucket 管理。

storage模块迁移`storage_files`、`storage_tasks`和T09新增的`storage_references`，不读取artifact表，不建跨域外键。普通文件操作经过IdentityResolver，bucket管理仍经过TenantServiceVerifier。默认单文件上限1 GiB、准备期限24小时、请求体8 MiB；显式StorageFiles配置需三个值均为正。配置样例见`conf/server/config.toml`。

登记和确认请求/成功响应均为顶层数组，1–1000项。SDK调用没有整体error仍需逐项检查Error；结构、重复项和入口身份错误整体失败。登记按可信租户、精确项目scope、用户和requestKey幂等；重放保留原期限和终态。文件位置由storage分配且不在普通响应中暴露，未准备bucket时不会自动创建。

确认只Stat存在与大小，保存客户端可选声明摘要，不读取或核验文件内容。对象缺失/大小不符可修正后重试；Stat结束后仍在短事务核对状态与原期限。显式删除未绑定文件返回202，worker完成物理清理后变为deleted；已deleted重复删除200，bound拒绝409（T09已接入引用创建）。

storage worker独立领取持久任务、续租和检查领取token；先等待LastGrantExpiresAt，再中止精确key的未完成分片和删除对象。默认每小时检查deleted墓碑的迟到对象/分片；发现残留或无法确认清理状态时进入有界删除任务，failed不自动重启。物理删除回执不代表JuiceFS trash立即释放磁盘块。

维护命令增加`--domain storage`，其余参数同artifact域；使用storage数据库的私有配置。默认dry-run，执行需expectedRevision和操作人/原因/记录号，原任务义务、NotBefore及累计次数保留，恢复审计与重排同事务提交。

[T07验收记录](docs/t07-acceptance-20261015.md)区分受控S3故障测试和真实JuiceFS验收。T07验收时上传由测试管理身份操作S3；本轮T08新增用户grants和客户端直传授权。

## T08 统一 grants 与直传

`POST /v1/storage/grants`接受对象请求：`fileIDs`、`scope`及可选正整数`ttlSeconds`。scope使用`{"preset":"upload"}`、`{"preset":"download"}`或`{"actions":["s3:GetObject"]}`，三者不混用。upload展开为PutObject、ListMultipartUploadParts、AbortMultipartUpload；download为GetObject（包括GET、HEAD和Range）。自定义只允许这四个精确S3动作，不接收policy、通配符或客户端bucket/key。

整份请求返回一组STS凭据及按输入顺序排列的精确对象位置，任一文件不合法就整份拒绝。策略超过2048字节返回GrantScopeTooLarge，由客户端拆批；不扩大到前缀或整桶。SDK方法为`storagev1.CreateGrant`，上传和下载字节仍由客户端自己的S3 SDK处理，文件夹按精确文件key集合授权。

启用需要配置`Storage.GrantIssuer`中的独立普通IAM凭据及STS端点；未配置时不安装grants路由。不能复用bucket管理/root key，其基础策略需要允许目标租户桶的相应操作，session policy再收窄到本次动作和精确key。当前STS协议配置按已验收的JuiceFS AssumeRole行为实现，不包含AWS角色ARN选择。临时issuer还必须提供真实ExpiresAt。示例见`conf/server/config.toml`，凭据通过私有环境注入。

StorageGrants默认UploadTTL/ReadTTL均1小时，同时作为请求的上限；显式配置需两个值均为正。AllowedActions省略允许四项，空数组禁用所有动作，已知但被部署禁用的动作返回403 GrantScopeDenied，不静默删减请求权限。配置切片在构造时复制。

上传动作要求active、uploading和原准备期限未过。先在数据库事务中预留LastGrantExpiresAt，再签发，最后重新认证并锁内复查；失败不缩短预留。ready文件可读但受原准备期限约束，bound不受旧准备期限限制；读取不更新LastGrantExpiresAt，也不阻止已通过授权检查后发生的清理。T09验证单文件引用绑定链，T10已补齐草稿及完整多文件业务。

STS实际到期不得超过锁内确定的截止时间；签发前扣5秒预算，剩余不足900秒返回GrantTTLTooShort，不向上凑整。显式请求900秒通常不能满足该预算。确认后不能再申请上传权限，已发凭据自然到期，服务不保证撤销或禁止凭据有效期内重写。

[T08验收记录](docs/t08-acceptance-20261015.md)记录权限反例、并发和真实后端证据；引用交接和完整带文件发布见 T09/T10。

## T09 持久引用交接与单文件发布

当前单服务实现及部署验证见[进程内文件引用](docs/inprocess-storage-20261016.md)。

文件元数据查询、Bind、Release 和引用状态查询通过 `internal/app/storage.ReferenceService` 进程内调用，由 storage 的 `BindingService` 执行。业务位于 `internal/app/storage`，S3 客户端位于 `internal/client/storage`，storage 库表模型位于 `internal/table/storage`；引用类型直接归 storage app，不再单设 `fileref` 包。服务不再为文件引用启动内部 HTTP 监听，也不需要引用交接 token、TLS 或 scope allowlist。公开文件操作仍验证用户身份和精确租户/项目归属，租户 bucket 管理保留独立的服务身份验证。

启用 StorageDB 时，`StorageConsumerID` 必须显式提供非空且至多 64 字节的值。它固定引用的持久归属，构造绑定服务时注入；请求不能自行选择。已有部署必须保留原值（OrbStack 为 `artifact-orbstack`），以便继续查询和释放已有引用。示例见 `conf/server/config.toml`。

创建版本可携带已确认文件清单，例如单文件：`files: [{"path":"model.bin","fileID":"..."}]`。artifact 先持久保存 ReferenceChange 计划，再调用 storage Bind，最后在本地事务提交版本清单、编号、alias、retention 和成功回执。业务清单仅保存 FileID、ReferenceID 和确认元数据，不保存 bucket/key。编号仍只在最终成功事务中分配。

同 requestKey 的有效 attempt 返回 RequestInProgress；已成功请求原样重放同一版本。attempt 固定 120 秒，不续期；失联恢复只中止并清理，不替客户端发布。明确失败会持久保存全部计划引用的释放义务，最早释放时间不早于文件原始准备期限；旧任务不会覆盖新 attempt 的引用。

带文件版本逻辑删除时同事务写释放任务；storage 最后引用释放后才回收物理文件。非最后引用释放结果稳定完成，最后引用等待物理删除回执，引用墓碑长期保留。任务沿用有界失败预算和 maintenance 人工重排。

当前支持多文件/文件夹、带文件草稿及复制；T09 记录的是单文件直接发布阶段。[T09 验收记录](docs/t09-acceptance-20261015.md)记录独立进程、双库和故障恢复证据。

## T10 带文件草稿与完整发布

草稿保存JSON与完整文件清单，共享Revision；改路径保留引用，替换/移除异步释放。复制版本和发布指定草稿修订为目标增加独立引用，成功不修改来源草稿。文件可在同scope跨集合复用，最后引用释放后才物理回收。双页CAS冲突先持久中止，新增附件至少保留原准备窗口供另存。显式大JSON沿普通文件路径，不自动转存payload。实现、双库故障回归和review结果见[T10验收记录](docs/t10-acceptance-20261015.md)。

## T11 持久 URI 与 Resolve

`POST /v1/resolve` / `artifactv1.Client.Resolve`按当前可信精确作用域解析集合、固定版本、alias/latest及文件URI，返回同一数据库快照中的业务元数据。alias结果包含实际固定版本URI，逻辑删除的目标不可解析；Resolve不签发权限、不读取文件字节、不创建保留义务。OpenAPI包含全部27个业务操作。见[T11验收记录](docs/t11-acceptance-20261015.md)。

当前实现的设计与 Spec 对照结果见 [跨模块 Review](docs/cross-module-review-20261015.md)，包含本轮运行观测修正、验证结果和外部验收限制。

## T12 交付验收

完整双库 race、维护 CLI、成对数据库恢复、镜像构建和真实 JuiceFS 文件草稿发布/复用/到期回收已通过。运行方式见 [部署说明](deploy/artifact/README.md)，Spec 对照见 [验收矩阵](docs/t12-delivery.md)。默认 server 缺可信身份 resolver 时拒绝启动；Forge session 接入与本机四层联合恢复已完成本地验证，见[接入交付](docs/tenant-integration-acceptance-20261016.md)和[恢复记录](docs/four-layer-recovery-20261016.md)。本机真实 Casdoor 登录、文件链路和[重复部署](docs/deployment-consolidation-20261016.md)已通过；生产网络/密钥与生产恢复目标仍待验收。此前 TLS/凭据轮换见[部署验收记录](docs/deployment-acceptance-20261015.md)。
