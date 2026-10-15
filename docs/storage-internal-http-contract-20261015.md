# storage 内部 HTTP/JSON 交接契约

日期：2026-10-15。状态：HTTP/JSON、具体路由、认证方式和开发默认值均已由用户确认；内部交接已在T09实现，实际部署验收归T12。本文细化[引用交接协议](storage-reference-handoff-design-20261015.md)，不改变其中的事务、补偿和成功条件。

## 边界与认证

首期即通过 HTTP/JSON 调用，combined、artifact-only、storage-only 使用相同传输契约。combined 可以连接本机独立内部监听地址，但不能走共享事务或直接调用 table 的快捷路径。以下四个操作只注册在受服务认证保护的内部监听入口，不计入 27 个公开操作，也不放进用户 storagev1 SDK。 combined使用独立回环内部监听，先就绪再接收需要交接的业务请求；退出时先停止新业务入口，再有界等待worker，最后关闭内部HTTP和两域数据库。纯JSON的artifact-only启动不要求装配storage数据库或S3。

已确认方案：复用已查到的配置型 Bearer 服务凭据接入方式，生产或跨主机通过 TLS 传输，同机回环开发允许 HTTP；storage 验证凭据，并通过部署配置映射为稳定 ConsumerID 和允许的精确作用域集合。为 artifact→storage 单独分配 consumer 凭据，不复用租户系统管理员凭据，不自行签发新的 token 协议。凭据轮换、进程重启、多副本不改变 ConsumerID；不同逻辑消费方使用不同身份。将来采用标准 TLS 双向认证（mTLS）时，只替换服务身份验证实现，保持相同验证结果。现有接入方式的源码证据见 [T00 身份接入报告](t00-identity-baseline-20261015.md)；本项目内部 TLS、凭据配置及轮换尚未演练。

请求中的 scope 是受认证服务声明的操作目标，storage 必须检查其位于服务身份允许范围，随后逐文件检查精确归属。普通用户不能调用这些接口；不能从未经验证的 Header 或请求体读取 ConsumerID。tenantID 必填，projectID 必填且为项目 ID 或 null；null 只代表租户级，不代表该租户所有项目。首期不定义通配作用域或跨 scope 批次。新增 scope 必须由可信接入配置授予；不让客户端增加自己的权限。

Release 和状态查询必须允许服务处理其授权范围内已 inactive/deleted 的历史租户/项目；撤销终端用户访问不应阻断已经持久记录的释放义务。服务认证本身仍须有效。Bind 的业务 active 条件由 artifact 在准备与最终提交检查，storage 按自身文件状态、精确 scope 和服务授权判断，不回调 artifact。

不传 ArtifactID、VersionID、DraftID、业务 owner、用户 token、数据库 tx，也不把 bucket/objectKey 交给 artifact。HTTP 追踪 ID 可以贯通日志，但不能作为权限或幂等键。

## 共同编码与限制

- Content-Type 为 application/json；时间为 UTC RFC3339，字节数为非负 JSON 整数。ReferenceID 和 FileID 沿用系统 ID 契约。
- 每批 1–1000 项，总请求体最多 8 MiB。fileIDs 不重复；references 中 ReferenceID 不重复，允许不同 ReferenceID 指向同一 FileID。每个批次只能操作一个精确 scope。
- 请求保留 scope 加 fileIDs/references 的对象：scope 是真实的批级条件。成功响应直接为结果数组，与输入等长、同序，不再增加 items/data 包装。
- malformed JSON、缺少字段、null/非数组列表、空列表、超限、重复 ID、未知字段或元素格式错误，在任何变更前整体拒绝。大小限制为 413，其余格式错误为 400。
- Bind、Release 批内原子；任一项不满足条件则本批不提交。准备查询也先整体校验身份、归属与存在性，再返回结果。与公开文件登记/确认的逐项成功语义不同。
- 所有读取使用 storage 主库。每次响应反映读取时的状态，不对随后的另一批操作提供状态承诺。

## 四个操作

| 操作 | 方法与内部路径 | 作用 |
| --- | --- | --- |
| GetReferenceFileMetadata | POST /internal/v1/storage/reference-files/query | 准备阶段读取可信文件元数据及原始期限 |
| BindReferences | POST /internal/v1/storage/references/bind | 原子建立一批持久引用 |
| ReleaseReferences | POST /internal/v1/storage/references/release | 原子终止一批引用或写释放墓碑 |
| GetReferenceStatuses | POST /internal/v1/storage/references/query | 查询本 consumer 的稳定引用回执 |

四项都是同步返回 HTTP 200 的数据库操作。Release 的 200 表示本批引用释放决定已经提交，物理删除可能仍在进行。没有 202 远程作业、内部 taskID 或额外 commit 接口。

### 准备文件元数据

请求：

```json
{
  "scope": {"tenantID": "t01", "projectID": "p01"},
  "fileIDs": ["f01"]
}
```

响应：

```json
[
  {
    "fileID": "f01",
    "state": "ready",
    "size": 1048576,
    "contentType": "application/octet-stream",
    "declaredChecksum": null,
    "originalExpiresAt": "2026-10-16T00:00:00Z"
  }
]
```

size 是 storage 确认的大小，尚未确认时为 null；未确认文件进入 deleting/deleted 后仍为 null，ready/bound 必须有确认大小。contentType 和 declaredChecksum 沿用文件公开契约，声明摘要未经服务端内容验证。state 可为 uploading、ready、bound、deleting、deleted；文件墓碑保留原始期限。缺失或跨 scope 文件整体 404。查询不建立引用、不续期，不执行 S3 Stat，也不是绑定成功保证。

artifact 必须在第一次 Bind 之前持久保存每个新增引用的 originalExpiresAt。只选择未过期 ready 或已有有效引用的 bound 进入绑定计划；storage 在 Bind 事务锁内再次判断。bound 的原始期限可能已过去，不影响建立新引用，但失败时也不会凭空得到新准备窗口。

### BindReferences

请求：

```json
{
  "scope": {"tenantID": "t01", "projectID": "p01"},
  "references": [{"referenceID": "r01", "fileID": "f01"}]
}
```

响应：

```json
[
  {
    "referenceID": "r01",
    "fileID": "f01",
    "state": "active",
    "file": {
      "size": 1048576,
      "contentType": "application/octet-stream",
      "declaredChecksum": null,
      "originalExpiresAt": "2026-10-16T00:00:00Z"
    }
  }
]
```

storage 使用已认证 ConsumerID 与 ReferenceID 作为唯一身份。相同引用已 active 且 FileID/scope 相同则返回成功，不重复增加引用；不同关联返回 409 ReferenceConflict；已 released 永久返回 409 ReferenceReleased。新引用只允许未过期 ready 或有有效引用的 bound；其余状态返回 409 FileNotBindable。

所有文件按 FileID 升序加锁，与 Release/GC 使用相同顺序。事务提交后才返回成功。metadata 响应不能替代准备阶段的持久原始期限；Bind 响应可能丢失。

### ReleaseReferences

请求与 Bind 相同。每个结果使用以下结构：

```json
[
  {
    "referenceID": "r01",
    "fileID": "f01",
    "state": "released",
    "releaseComplete": false,
    "releaseRequiresDeletion": true
  }
]
```

- 释放 active 引用：存在其他引用则 releaseComplete=true、releaseRequiresDeletion=false，该回执以后保持完成；最后引用则 releaseRequiresDeletion=true，等待 storage 物理删除回执后 releaseComplete 才为 true。
- 引用未知：在文件存在且精确归属合法时写 released 墓碑，releaseComplete=true、releaseRequiresDeletion=false。不能因为没有实际引用被释放，就把 preparing/ready 文件直接转 deleting。
- 重复释放：返回该引用现有状态及回执，不再改变计数。同一 ReferenceID 不同 FileID/scope 返回 409；缺失或跨 scope 文件返回 404。
- 文件已经 deleted 时仍可查询/重复释放已有墓碑；文件身份行和引用墓碑保留。未知引用关联一个合法 deleted 文件也可以写终态墓碑，以拒绝迟到 Bind。

NotBefore 属于 artifact 的持久释放任务，不传给 storage，也不让 storage 推测补偿原因。只有 artifact 确认中止，并等待 max(now, originalExpiresAt) 到达后才发送失败 attempt 的全部引用。不能根据 GetReferenceStatuses 的 absent 提前发送；迟到 Bind 仍可能随后提交。

### GetReferenceStatuses

请求与 Bind 相同，以 referenceID/fileID 对核对预期关联。成功响应直接为结果数组；每项包含 referenceID、fileID、state、releaseComplete、releaseRequiresDeletion：

| state | releaseComplete | releaseRequiresDeletion | 含义 |
| --- | --- | --- | --- |
| absent | false | null | 本 consumer 下该引用未知，不能据此认定补偿完成 |
| active | false | null | 持有关系仍有效 |
| released | true | false | 非最后引用或从未绑定的墓碑，固定完成 |
| released | false | true | 最后引用释放，仍等物理删除回执 |
| released | true | true | 最后引用释放且物理删除回执已确认 |

unknown ReferenceID 返回 absent，不返回全批 404；但请求中的 FileID 仍须存在且归属正确，不泄露其他 scope 文件。同 ReferenceID 已存在但关联不同返回 409 ReferenceConflict。一个 consumer 不能查询或释放另一个 consumer 的引用：引用命名空间以认证主体隔离；在自己命名空间写同名墓碑不会影响其他 consumer。

所有 released 墓碑首期无限期保留。文件 deleted 后仍从引用记录和保留的文件身份取得同一释放回执，不能将其降级为 absent，也不能重新等待另一个引用的删除。

## 错误、重放和截止时间

错误响应复用技术设计中的服务标准错误对象（code/message/requestId/statusCode）；HTTP状态取响应行，statusCode不等于HTTP状态。下表列HTTP状态与稳定业务code，SDK不依赖message文案：

| HTTP | code | 调用方动作 |
| --- | --- | --- |
| 400 / 413 | InvalidRequest / RequestBodyTooLarge | 修正输入或缩小批次，不自动重试 |
| 401 / 403 | Unauthenticated / ScopeForbidden | 修复服务认证或授权，不自动放宽 scope |
| 404 | FileNotFound | 缺失或跨 scope 文件，不暴露真实归属 |
| 409 | ReferenceConflict / ReferenceReleased / FileNotBindable | 本批确定拒绝，按 artifact 持久决策进入失败处理 |
| 429 | TooManyRequests | 尊重 Retry-After，保持原引用集合和身份重试 |
| 503 | StorageUnavailable | 暂不可用；若变更调用结果不明，按结果未知处理 |
| 503 | StorageOutcomeUnknown | storage 事务提交结果不能确认；保留义务，查询或原样重放 |

只有已经确定本批没有提交的情况才能返回表示确定拒绝的 4xx。数据库 commit 失联不能伪装成 FileNotBindable。可使用标准 Retry-After 表达等待建议，不新增自定义重试协议。传输断开、超时、响应格式损坏及其他无法判断结果的 5xx，统一按可能提交处理。

已确认开发默认值：连接超时 1 秒、每次完整 HTTP 调用 5 秒；每次同步调用含初次最多 3 次尝试，退避 200 ms、500 ms 并加入抖动，仅在尚有调用方截止时间时重试。artifact attempt 总期限为 120 秒且不自动续期。调用方必须在剩余 attempt 时间内发 Bind，远端超时或取消不证明事务已经回滚；单次 HTTP 超时不直接把业务判失败。后台 Release 由持久任务重试预算控制，进程内重试同样有限，不在 HTTP client 内无限等待。

Bind/Release 不增加 requestKey：持久的 ReferenceID 和不可变关联就是幂等身份。同批可原样重放，也可将不确定的一批拆成较小批次重放，但不得改 ReferenceID/FileID/scope。若重放 Bind 遇到已 released，不能恢复旧 attempt；若 Release 结果未知，仍等待明确 released 回执。

HTTP 请求取消只停止等待和可取消工作，不是引用撤销命令。artifact attempt 的 committed/aborted 决策只由自身主库决定；最终业务提交未知时，内部 storage 状态不能证明业务失败。恢复 worker 只中止过期准备和投递释放，不后台发布。两域都不能把另一方事务带进 HTTP handler。

## T00 核对结果与实施验证

本次完成文档规则核对：

- 四个内部操作覆盖准备元数据与三个已定交接操作；公开 27 项不增加。
- 文件元数据不包含业务身份、S3 位置或业务回调；scope 是服务认证授权后的精确目标。
- 多批失败、Release 先到、原准备期限补偿、released 永不复活及文件删除后回执，均保留原交接协议约束。
- 单次超时、attempt 超时和后台租约不混为一个期限；没有传输失败自动补偿已提交业务的路径。
- 数组结果与共同 scope 请求有明确职责；内部批次原子性不会误套公开登记/确认的逐项结果语义。

T09已实现内部handler、客户端、consumer认证及两域独立数据库交接，具体运行证据与边界见[T09验收记录](t09-acceptance-20261015.md)。已验收两进程/两数据库、丢响应、取消、乱序、分批、未知提交与稳定墓碑；真实部署证书和服务凭据轮换演练仍归T12。

上表路由、TLS与独立Bearer服务凭据、批次/大小上限及超时重试默认值均已获批；保持HTTP/JSON、独立事务、引用生命周期及补偿规则不变。
