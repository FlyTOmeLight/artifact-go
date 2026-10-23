# artifact-go 接入使用手册

面向需要读写物料集合、版本与文件的其他 Go module。本文按调用顺序组织，只描述当前代码的公开契约；字段的完整定义以 `docs/openapi.json`（Swagger 2.0，18 条路径、27 个操作）和 `sdk/go/` 源码为准。

## 1. 前置条件

- 服务已由部署方完成装配：`service.New` / `service.BuildWithIdentity` 必须传入 `IdentityResolver`（`service/identity.go`）。`cmd/server` 缺少 resolver 时拒绝启动，SDK 无法绕开这一步。
- 端点和 TLS 由接入层提供，SDK 只接收 `Endpoint`。
- SDK 与服务在同一 module：`github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1`（集合、版本、草稿、alias、保留、Resolve）和 `sdk/go/storagev1`（bucket、文件、授权）。要求 Go 1.27.1。
- 文件字节不经过本服务。服务只登记元数据、签发临时 S3 凭据，传输由调用方自己的 S3 客户端完成。

## 2. 快速开始

```go
import (
    "context"
    "time"

    "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
)

client, err := artifactv1.NewClient(artifactv1.ClientConfig{
    Endpoint: "http://127.0.0.1:18080",
    Timeout:  10 * time.Second,
})
if err != nil {
    return err
}
defer client.Close()

ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
ctx = artifactv1.WithRequestIdentity(ctx, artifactv1.RequestIdentity{
    BearerToken: token,
    TenantID:    tenantID,
})

created, err := client.CreateArtifact(ctx, &artifactv1.CreateArtifactRequest{
    Type:          "model",
    Name:          "demo",
    VersionScheme: artifactv1.VersionSchemeInteger,
    RequestKey:    "create-demo-1",
})
if err != nil {
    return err
}

version, err := client.CreateVersion(ctx, &artifactv1.CreateVersionRequest{
    ArtifactPath: artifactv1.ArtifactPath{ArtifactID: created.ID},
    RequestKey:   "publish-demo-1",
    Payload:      []byte(`{"weight":0.5}`),
})
if err != nil {
    return err
}
```

`client` 可并发复用；身份属于 context，不属于 client。后文示例里的 `ptr(v)` 是调用方自备的泛型辅助函数（`func ptr[T any](v T) *T { return &v }`），SDK 不提供。

## 3. 身份与范围

每次调用用 `WithRequestIdentity` 传入本次身份。SDK 把它写成请求头，服务端的 resolver 负责认证并核对范围；头本身不授权。

| 字段 | 含义 |
| --- | --- |
| `BearerToken` | 通用服务/用户令牌；Forge 接入不使用它表示登录会话 |
| `Session` | `forge_session` Cookie 值（仅 Forge 接入） |
| `CSRFToken` / `Origin` | 写请求的原始 CSRF 与 Origin（仅 Forge 接入） |
| `TenantID` | 选择的租户，必填 |
| `ProjectID` | 选择的项目；`nil` 表示租户级 |
| `ResourceUserID` | 选择的资源归属用户；`nil` 表示共享 |
| `RequestID` | 写入 `X-Request-ID`，便于关联日志 |

四种精确范围，空值都是明确范围，不是通配符：

| `ProjectID` | `ResourceUserID` | 范围 |
| --- | --- | --- |
| `nil` | `nil` | 租户共享 |
| 有值 | `nil` | 项目共享 |
| `nil` | 有值 | 租户内个人 |
| 有值 | 有值 | 项目内个人 |

要点：

- 两个 SDK 的 `ProjectID` 类型不同：`artifactv1.RequestIdentity.ProjectID` 是 `*string`，`storagev1.RequestIdentity.ProjectID` 是 `string`。同一业务范围的两次调用要分别写 `&projectID` 和 `projectID`。
- `artifactv1` 只在 `ProjectID != nil && *ProjectID != ""` 时发送项目头，所以指向空字符串的指针不会发送项目头；表示租户范围请直接传 `nil`。
- `ResourceUserID` 非 nil 就发送；显式空值由服务端拒绝：`400 InvalidScope`。`X-Resource-User-ID` 出现多个值时同样拒绝。
- resolver 返回的 `ResourceUserID` 与 `X-Resource-User-ID` 选择不一致时，服务端返回 `403 ScopeMismatch`，而不是降级成共享。这正是保护旧 resolver 的手段：不认得个人范围就拒绝。
- 租户与项目的一致性由 resolver 自己核对；典型做法是不一致时同样返回 `403 ScopeMismatch`。
- 范围不匹配的其他资源一律 `404`，不泄露其他范围是否存在。
- `OwnershipState=deleted` 的范围仍允许读取、归档、收回或删除草稿、管理既有版本的注释/保护/删除、改绑 alias、更新保留策略；新建集合与发布版本、新建或保存草稿、登记文件和申请上传授权返回 `403 ScopeInactive`。
- `CreateArtifactRequest.ProjectID` 只用于断言，与可信范围不一致时返回 `403 ScopeMismatch`；一般留空，范围完全由 `RequestIdentity` 决定。

## 4. 客户端行为

- 不自动重试任何请求（`Retry: 0`），有副作用调用失败后由调用方决定。
- 不保留 Cookie，拒绝 HTTP 重定向。凭据不会被带到别的地址。
- `WithRequestIdentity` 在放入 context 时复制指针所指的值，调用方之后修改原变量不影响已发出的请求。
- 每次调用都读取当次 context 里的身份，因此同一个 client 可以同时服务多个用户。
- 未设置 `Timeout` 时使用框架默认值。

## 5. 集合

| 操作 | SDK 方法 | 结果 |
| --- | --- | --- |
| 创建 | `CreateArtifact` | 首次 201，同键重放 200 |
| 列表 | `ListArtifacts` | 200，按 ID 升序 |
| 读取 | `GetArtifact` | 200，含归档集合 |
| 归档 | `ArchiveArtifact` | 200，幂等；不删除名称和编号身份 |

- `Type` 与 `Name` 是 ASCII 标识符：`type` 为 `^[a-z][a-z0-9_-]{0,63}$`，`name` 先转小写再按 `^[a-z][a-z0-9_-]{0,127}$` 校验。同一完整范围 + `type` 内名称唯一。
- `VersionScheme` 取 `integer` 或 `semver`。`InitialVersion` 省略时分别为 `1` 和 `1.0.0`；integer 只接受 `1`，semver 必须是三段数字。
- `RequestKey` 可选（≤64 字符）。省略则每次创建都是新集合，提交结果不明时无法安全恢复；有稳定业务 ID 的调用方应当传。
- 同键合法异参返回 `409 IdempotentParameterMismatch`；不同键但同范围同 `type`/`name` 返回 `409 ArtifactNameConflict`。归档后重放成功键仍返回 200 和当前（归档）状态。
- 响应带 `ArtifactURI`、`retentionPolicy`（未配置时 `revision=0`、`enabled=false`）。

## 6. 版本

`CreateVersion` 有两种互斥的内容来源。

**直接内容**：`Payload` / `Annotations` 用 `json.RawMessage` 传 JSON 对象（省略视为 `{}`，`null` 拒绝），`Files` 传完整文件清单。

```go
version, err := client.CreateVersion(ctx, &artifactv1.CreateVersionRequest{
    ArtifactPath: artifactv1.ArtifactPath{ArtifactID: artifactID},
    RequestKey:   "publish-2026-10-23-1",
    Payload:      []byte(`{"score":0.91}`),
    Annotations:  []byte(`{"note":"baseline"}`),
    Files: []artifactv1.FileReference{
        {Path: "model.bin", FileID: fileID},
    },
    Protected: true,
    Aliases: []artifactv1.AliasUpdate{
        {Name: "prod", ExpectedRevision: ptr(int64(0))},
    },
})
```

**草稿修订**：`DraftID` 与 `ExpectedDraftRevision` 必须同时提供，且不能再带 `Payload` / `Annotations` / `Files`。发布不修改草稿。

```go
version, err := client.CreateVersion(ctx, &artifactv1.CreateVersionRequest{
    ArtifactPath:          artifactv1.ArtifactPath{ArtifactID: artifactID},
    RequestKey:            "publish-from-draft-1",
    DraftID:               &draftID,
    ExpectedDraftRevision: &draftRevision,
})
```

规则：

- `RequestKey` 必填（1–64 字符）。同集合、同可信用户、同键同参返回原版本（201/200 区分首次与重放）；异参 `409 IdempotentParameterMismatch`。
- 编号：integer 从 1 递增，不接受 `Bump`；semver 的首次发布只接受 `patch`（或省略），之后省略 `Bump` 等同 `patch`。
- `Files` 中 `Path` 是逻辑路径（相对、非空段、不能有 `.`/`..`/反斜杠/`\x00`，≤512 字符），`FileID` 必须属于同一范围且已 `ready` 或 `bound`，`ready` 文件还要在原准备期限之内。清单内路径和文件 ID 都不能重复；`Path` 不唯一时发布失败。
- 带文件的请求会先登记引用再提交版本；同一 `requestKey` 的有效尝试期间返回 `409 RequestInProgress`（带 `Retry-After: 1`），尝试过期后返回 `409 RequestAttemptExpired`，可用同一键重来。
- 归档集合拒绝发布新版本：`409 ArtifactArchived`。
- `Protected` 与 `Aliases` 在同一次发布中原子提交；重放不会再次移动 alias，也不会覆盖后续管理操作。
- 版本状态：`ready`、`deleting`（等待文件释放）、`deleted`。列表和普通读取只返回 `ready`。

管理与删除：

| 操作 | 方法 | 说明 |
| --- | --- | --- |
| 列表 | `ListVersions` | 按 `Sequence` 降序，可按 `Number` 过滤 |
| 读取 | `GetVersion` | 只返回 ready |
| 改注释 | `UpdateAnnotations` | `annotations` 必须非空，完整替换 |
| 改保护 | `UpdateProtection` | `protected` 必须显式 |
| 删除 | `DeleteVersion` | `200`，返回墓碑与状态 |

`UpdateAnnotations` 与 `UpdateProtection` 共享 `Revision`：两者都用 `ExpectedRevision` 做 CAS，并发时只有一方成功，落后方 `409 RevisionConflict`。显式零修订同样是过期修订。

`DeleteVersion` 清空 payload/annotations 但保留 ID、编号和成功回执。`Protected` 版本返回 `409 VersionProtected`，仍被 alias 引用时返回 `409 VersionReferenced`（先删或改绑 alias）。纯 JSON 版本直接变为 `deleted`；带文件的版本进入 `deleting`，等 storage 释放并回收后才变为 `deleted`。重复删除返回当前墓碑。

### alias

```go
_, err := client.PutAlias(ctx, &artifactv1.PutAliasRequest{
    AliasPath:        artifactv1.AliasPath{ArtifactPath: artifactv1.ArtifactPath{ArtifactID: artifactID}, Name: "prod"},
    VersionID:        version.ID,
    ExpectedRevision: ptr(int64(0)), // 首次绑定用 0
})
```

- 名称是小写 ASCII 标识符，≤64 字符；`latest` 只读，由服务端动态计算最高 `Sequence` 的 ready 版本。
- 首次绑定 `ExpectedRevision=0`，之后改绑、删除、从墓碑恢复都必须用当前 `Revision`；不一致返回 `409 RevisionConflict`。绑定的目标必须是 ready 版本，否则 `404 VersionNotFound`。
- `DeleteAlias` 通过 query 传 `expectedRevision`，返回 200 和墓碑（`deletedAt` 非空，`target` 为 null）。
- `GetAlias` 的 `IncludeDeleted` 用于读取墓碑；别名从未存在或已删除且未带该参数时返回 `404 AliasNotFound`。

## 7. 草稿

```go
draft, err := client.CreateDraft(ctx, &artifactv1.CreateDraftRequest{
    ArtifactPath: artifactv1.ArtifactPath{ArtifactID: artifactID},
    RequestKey:   "draft-new-1",
    Payload:      []byte(`{"wip":true}`),
})

_, err = client.UpdateDraft(ctx, &artifactv1.UpdateDraftRequest{
    DraftPath:        artifactv1.DraftPath{ArtifactPath: artifactv1.ArtifactPath{ArtifactID: artifactID}, DraftID: draft.ID},
    RequestKey:       "draft-save-1",
    ExpectedRevision: ptr(draft.Revision),
    Payload:          []byte(`{"wip":false}`),
    Annotations:      []byte(`{}`),
    Files:            []artifactv1.FileReference{}, // 非 nil；空切片表示移除全部文件
})
```

- 创建时可以直接给内容、用 `SourceVersionID` 复制 ready 版本，或只给 `requestKey`。`SourceVersionID` 与直接字段互斥。
- 创建与保存都用 `RequestKey` 幂等：同键同参重放原结果，异参 `409 IdempotentParameterMismatch`；带文件时同样可能出现 `409 RequestInProgress` / `409 RequestAttemptExpired`。
- 保存必须完整提供 `payload`、`annotations`、`files`、`expectedRevision`、`requestKey`；`files` 为 nil 会被拒绝（`400`），要清空就传 `[]`。
- `UpdateDraft` 返回 `UpdateDraftResponse`：`AppliedRevision` 是本次成功写入的修订，`Draft.Revision` 是当前快照修订。重放旧请求时 `AppliedRevision` 仍是最初的成功修订，不能把新修订号套到旧内容上。
- 同一修订并发保存只有一方成功，另一方 `409 RevisionConflict`。
- `DeleteDraft` 用 query 的 `expectedRevision`，成功 204。删除后原创建/保存请求重放返回 `410 DraftDeleted` 及原身份。
- 历史范围与归档集合允许已授权的读取、成功重放和条件删除，不允许首次创建或新保存。

## 8. 保留策略

```go
_, err := client.UpdateRetention(ctx, &artifactv1.UpdateRetentionRequest{
    ArtifactPath:     artifactv1.ArtifactPath{ArtifactID: artifactID},
    ExpectedRevision: ptr(created.RetentionPolicy.Revision), // 用当前策略修订；首次配置为 0
    Enabled:          ptr(true),
    MaxVersions:      ptr(int64(10)),
    MaxAgeSeconds:    nil, // 显式 null
    MinVersions:      ptr(int64(2)),
    OverflowAction:   artifactv1.OverflowReject,
    ProtectAliases:   ptr(true),
})
```

- 完整替换：`enabled`、`minVersions`、`protectAliases`、`overflowAction`、`expectedRevision` 都是必需的指针（或枚举）字段；`maxVersions` / `maxAgeSeconds` 用 `nil` 表示 `null`。
- `enabled=true` 时至少给一个上限；两个上限都为正；`minVersions <= maxVersions`。
- 发布超量时按 `overflowAction` 处理：`reject` 让发布失败（`409 RetentionBlocked`），`deleteOldest` 原子淘汰旧版本。新版本不参与同一次淘汰。
- `Protected` 版本总是受保护；`protectAliases` 决定自动淘汰是否解除普通 alias。
- `PreviewRetention` 只读，返回候选、保留原因、阻塞标志，不改变任何状态。首次配置前 `revision=0`，用 0 作为 `expectedRevision`。

## 9. 文件全链路

整体顺序：登记 → 申请上传授权 → S3 直传 → 确认 → 带 `FileID` 发布 → Resolve → 申请下载授权。

storage 调用要使用 storage 的客户端和它自己的身份类型，身份不能从 `artifactv1` 的 context 里继承：

```go
files, err := storagev1.NewClient(storagev1.ClientConfig{Endpoint: endpoint})
if err != nil {
    return err
}
defer files.Close()

sctx := storagev1.WithRequestIdentity(ctx, storagev1.RequestIdentity{
    BearerToken: token,
    TenantID:    tenantID,
    ProjectID:   projectID, // string，不是 *string
})
```

后面的 `RegisterFiles`、`CreateGrant`、`CompleteFiles` 都用 `sctx`。

### 9.1 登记

```go
size := int64(len(data))
registered, err := files.RegisterFiles(sctx, storagev1.RegisterFilesRequest{
    {RequestKey: "upload-config-1", Size: &size, ContentType: "application/json"},
})
if err != nil {
    return err
}
if len(registered) != 1 {
    return fmt.Errorf("unexpected register result count %d", len(registered))
}
if item := registered[0]; item.Error != nil { // HTTP 200 也可能有逐项失败
    return fmt.Errorf("register: %s %s", item.Error.Code, item.Error.Message)
}
fileID := registered[0].FileID
```

- 请求体是顶层数组，1–1000 项。每项的 `requestKey` 必填（1–64 字符），`size` 必须显式给出（含 0 字节），`contentType` 省略时为 `application/octet-stream`（≤255 字节）。
- `declaredChecksum` 可选，是客户端声明，不参与服务端校验。
- 返回同样是顶层数组，保持输入顺序。**调用返回 `nil` error 也要逐项检查 `Error`**；结构、重复键、入口身份错误才整体失败。
- 登记按可信范围 + 用户 + `requestKey` 幂等；重放保留原期限和终态。
- 文件在 `TemporaryFileTTL`（默认 24 小时）内必须完成确认并发布，`ExpiresAt` 会返回。

### 9.2 上传授权与直传

```go
grant, err := files.CreateGrant(sctx, &storagev1.CreateGrantRequest{
    FileIDs: []string{fileID},
    Scope:   storagev1.GrantScope{Preset: "upload"},
})
if err != nil {
    return err
}
s3Client := s3.New(s3.Options{
    Region:       grant.Region,
    BaseEndpoint: aws.String(grant.Endpoint),
    UsePathStyle: grant.ForcePathStyle,
    Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
        return aws.Credentials{
            AccessKeyID:     grant.Credentials.AccessKeyID,
            SecretAccessKey: grant.Credentials.SecretAccessKey,
            SessionToken:    grant.Credentials.SessionToken,
        }, nil
    }),
})
_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
    Bucket: aws.String(grant.Files[0].Bucket),
    Key:    aws.String(grant.Files[0].ObjectKey),
    Body:   bytes.NewReader(data),
})
```

- `CreateGrant` 只签发凭据，不上传、不创建分片任务、不重试签名。字节传输由调用方完成。
- `Scope` 必须是 `{"preset":"upload"}`、`{"preset":"download"}` 或 `{"actions":[...]}` 之一，不能混用。自定义动作只允许 `s3:PutObject`、`s3:ListMultipartUploadParts`、`s3:AbortMultipartUpload`、`s3:GetObject`。
- 上传动作要求文件处于 `uploading` 且未过准备期限（`409 FileNotUploading` / `409 FileExpired`）。
- 使用授权返回的 `Bucket` 和 `ObjectKey` 原样，不要自行拼接。
- `Endpoint` / `Region` / `ForcePathStyle` 直接用于构造 S3 客户端；`Files` 与请求的 `FileIDs` 顺序一致。
- 一批文件必须属于同一 tenant bucket，否则 `409 GrantLocationMismatch`。策略超过 2048 字节返回 `400 GrantScopeTooLarge`，应拆批。
- **凭据不得记录到日志、错误信息或追踪中**，`GrantCredentials` 是短期 STS 凭据。
- `ttlSeconds` 可省略。服务端默认上传/下载各 1 小时，显式值只能缩短、不能延长。STS 签发要求剩余时长扣除 5 秒预算后不少于 900 秒，所以 `ttlSeconds=900` 必然失败（`400 GrantTTLTooShort`）；需要更短生命周期时给 907 之类留出余量的值，否则直接省略。
- 确认之后不能再申请上传授权；已发出的凭据会自然到期，服务不保证撤销或禁止在有效期内重写。

### 9.3 确认

```go
completed, err := files.CompleteFiles(sctx, storagev1.CompleteFilesRequest{fileID})
if err != nil {
    return err
}
for _, item := range completed {
    if item.Error != nil {
        return fmt.Errorf("complete: %s %s", item.Error.Code, item.Error.Message)
    }
    if item.State != storagev1.FileReady {
        return fmt.Errorf("unexpected state %s", item.State)
    }
}
```

- 请求是顶层字符串数组（1–1000 个不重复 ID），返回逐项结果，同样要保持顺序并逐项检查。
- 确认只做 Stat：校验对象存在与大小一致，**不校验** `declaredChecksum`。
- `409 FileObjectMissing` / `409 FileSizeMismatch` 可修正后重试；`409 FileExpired` 需要重新登记。
- 已是 `ready` / `bound` 时确认是幂等的，直接返回当前元数据。

### 9.4 文件状态与回收

| 状态 | 含义 |
| --- | --- |
| `uploading` | 已登记，等待上传与确认 |
| `ready` | 已确认，可绑定到版本或草稿 |
| `bound` | 至少被一个版本/草稿引用 |
| `deleting` | 已请求删除，等待物理回收 |
| `deleted` | 物理回收完成 |

- `DeleteFile` 只接受未绑定文件：首次返回 202，已 `deleted` 再删返回 200；`bound` 返回 `409 FileReferenced`。
- 文件在最后一个引用释放后才物理回收。`deleting` 不等于 `deleted`。
- 同一文件可在同范围内跨集合复用；发布给版本或草稿的引用各自独立。
- 服务 Worker 会等待 `LastGrantExpiresAt` 再清理，避免删掉仍在有效凭据下可写的对象。

### 9.5 下载

```go
grant, err := files.CreateGrant(sctx, &storagev1.CreateGrantRequest{
    FileIDs: []string{fileID},
    Scope:   storagev1.GrantScope{Preset: "download"},
})
```

用同一个构造方式调用 `GetObject`，用完关闭 `Body`。读取要求文件是 `ready` 或 `bound`；`ready` 文件仍受原准备期限约束，过期返回 `409 FileExpired`，`bound` 不受该期限限制。`ExpiresAt` 之后不要再使用该凭据，重新申请即可。

## 10. 解析与稳定 URI

```go
_, err := client.Resolve(ctx, &artifactv1.ResolveRequest{URI: version.Files[0].FileURI})
```

URI 形态：

```text
artifact://<tenant>/artifacts/<type>/<id>
artifact://<tenant>/projects/<project>/artifacts/<type>/<id>
artifact://<tenant>[/projects/<project>]/artifacts/<type>/<id>/versions/<versionID>
artifact://<tenant>[/projects/<project>]/artifacts/<type>/<id>/aliases/<name>
artifact://<tenant>[/projects/<project>]/artifacts/<type>/<id>/{versions/<versionID>|aliases/<name>}/files/<path>
```

- 服务返回的 `ArtifactURI`、`VersionURI`、`FileURI` 直接保存和传递，不要手工拼接。
- `Resolve` 在当前可信范围内解析，返回同一数据库快照的 `Artifact`、可选 `Version`、可选 `File`。
- 它不签发权限、不读字节、不建立保留义务。要下载仍然走 `CreateGrant`。
- URI 里的租户/项目必须与当前身份一致，否则 `404`；格式错误返回 `400 InvalidURI`。
- `aliases/latest` 解析到当前最高 `Sequence` 的 ready 版本；逻辑删除的版本不可解析。

## 11. 幂等与不确定结果

| 场景 | HTTP | 处理 |
| --- | --- | --- |
| 首次成功 | 201 | 正常前进 |
| 同键同参重放 | 200 | 使用返回的原结果，不要重试别的键 |
| 同键异参 | 409 `IdempotentParameterMismatch` | 这是调用方 bug，换参数必须换键 |
| 并发同一请求 | 409 `RequestInProgress` | 等 `Retry-After` 后用**原键原参**重试 |
| 墓碑重放 | 410 `VersionDeleted` / `DraftDeleted` | 用 `errors.As` 取原身份，不要重建 |
| 提交结果不明 | 503 `OperationOutcomeUnknown` | 先读状态确认，再决定；不要盲目重写 |

- 幂等键空间包含完整范围和实际操作者。换用户或换范围就是新请求。
- 不带 `requestKey` 的集合创建不是幂等的：结果不明时先按 `type`/`name` 列表确认，再决定是否重建。
- `CompleteFiles`、`DeleteFile`、`DeleteVersion` 没有请求键，但按当前状态收尾：确认已完成时返回元数据，删除已完成时返回墓碑。注释、保护、alias、保留策略用 `expectedRevision` 做 CAS，重试前先读回最新修订。
- 已成功请求的重放不会重新移动 alias、覆盖保护状态或重新消耗编号。

## 12. 错误模型

所有非 2xx 返回一个错误对象：

```json
{"code":"RevisionConflict","message":"draft revision does not match","requestId":"...","statusCode":100409}
```

`statusCode` 是部署配置的 `AppCode`（样例为 100）与 HTTP 状态码拼接，不要硬编码解析；判断类型用 `code`。

```go
import "errors"

var apiErr *artifactv1.ErrorResponse // 类型别名，等同 server-go/errors.Error
if errors.As(err, &apiErr) {
    switch apiErr.Code {
    case "RevisionConflict":
        // 重新读取后重试
    case "OperationOutcomeUnknown":
        // 读取当前状态
    }
}
```

- `Status` 是 HTTP 状态码（不参与 JSON），`StatusCode` 是部署配置的应用码与状态码组合；判断是否重试用 `Code` 或 `Status`。
- `requestId` 与服务端日志对应，报障时带上。
- `storagev1` 没有导出错误别名，storage 的顶层错误需要用 `*github.com/FlyTOmeLight/server-go/errors.Error` 做 `errors.As`。

墓碑错误保留原身份：

```go
var deleted *artifactv1.VersionDeletedError
if errors.As(err, &deleted) {
    log.Printf("version %s (%s) was deleted", deleted.Version.ID, deleted.Version.Version)
}

var draftGone *artifactv1.DraftDeletedError
if errors.As(err, &draftGone) {
    log.Printf("draft %s deleted, applied revision %v", draftGone.Draft.ID, draftGone.AppliedRevision)
}
```

常见错误码：

| 码 | 状态 | 含义 |
| --- | --- | --- |
| `Unauthorized` / `InvalidIdentity` | 401 | 未提供可信身份或范围不合法 |
| `InvalidScope` | 400 | `X-Resource-User-ID` 为空、带空白或多个值 |
| `ScopeMismatch` | 403 | 请求选择与 resolver 授权范围不一致 |
| `IdempotentParameterMismatch` | 409 | 同一 `requestKey` 换了参数 |
| `RequestInProgress` | 409 | 同一请求仍在执行，原键原参重试 |
| `ScopeInactive` | 403 | 已删除范围上的新建或上传操作 |
| `ArtifactNotFound` / `ResourceNotFound` / `FileNotFound` / `AliasNotFound` / `VersionNotFound` | 404 | 不存在，或不在当前范围 |
| `ArtifactNameConflict` | 409 | 同范围同 type 下名称已存在 |
| `ArtifactArchived` | 409 | 归档集合拒绝新建版本/草稿 |
| `RevisionConflict` | 409 | CAS 修订不符 |
| `VersionProtected` / `VersionReferenced` | 409 | 受保护或被 alias 引用 |
| `RetentionBlocked` | 409 | 保留策略拒绝发布 |
| `FileNotUploading` / `FileNotReadable` / `FileExpired` | 409 | 文件状态不允许该授权 |
| `FileNotBindable` / `FileReferenced` | 409 | 文件不能绑定 / 已绑定不能删 |
| `FileObjectMissing` / `FileSizeMismatch` | 409 | 确认失败，可修正后重试 |
| `RequestAttemptExpired` | 409 | 发布或草稿尝试已过期；用同一 `requestKey` 可开启新尝试 |
| `GrantScopeDenied` / `GrantScopeTooLarge` / `GrantTTLTooShort` | 403/400 | 授权范围被部署禁用、策略过大、时长不足 |
| `InvalidParameter` / `InvalidVersionRequest` / `InvalidDraftRequest` / `InvalidAliasRequest` / `InvalidGrantRequest` / `InvalidURI` / `InvalidCursor` | 400 | 参数、URI 或游标不合法 |
| `PayloadTooLarge` / `AnnotationsTooLarge` / `FileTooLarge` / `RequestBodyTooLarge` | 413 | 超过配置上限 |
| `OperationOutcomeUnknown` | 503 | 提交结果未知 |
| `StorageUnavailable` / `GrantUnavailable` | 503 | 依赖未配置或不可用 |

批量接口（`RegisterFiles`、`CompleteFiles`）的逐项错误是 `storagev1.FileError`，字段为 `code`、`message`、`status`，不走顶层错误对象。

## 13. 分页与限制

- 列表统一支持 `Cursor` 和 `Limit`：`Limit` 默认 50，范围 1–200；服务端返回的 `NextCursor` 为空表示没有下一页。
- 游标绑定当前查询与范围（租户、scope、type、state、排序）。参数变了、换了范围或用错集合，返回 `400 InvalidCursor`。不要跨查询复用游标。
- 版本列表按 `Sequence` 降序，集合与草稿按 ID 升序。

| 限制 | 默认值 | 配置项 |
| --- | --- | --- |
| payload | 256 KiB | `MaxPayloadBytes` |
| annotations | 64 KiB | `MaxAnnotationsBytes` |
| 单个请求体 | 8 MiB | `MaxRequestBodyBytes` |
| 单版本/草稿文件数 | 1000 | `MaxFilesPerVersion` |
| 单文件 | 1 GiB | `StorageFiles.MaxFileBytes` |
| 文件准备期限 | 24 小时 | `StorageFiles.TemporaryFileTTL` |
| 授权默认时长 | 1 小时 | `StorageGrants.UploadTTL` / `ReadTTL` |
| 批量接口项数 | 1–1000 | 固定 |

大小按 `json.Compact` 后的 UTF-8 字节计算；整包按实际读取长度计算。成功重放不重新受字段大小限制，但仍受整包限制。

## 14. 两种接入方式

**Forge session 接入**：浏览器会话已经存在，接入层把原始 Cookie、`X-CSRF-Token`、`Origin` 交给部署方实现的 `IdentityResolver`，由它校验租户/项目成员与精确范围。SDK 侧用 `Session`、`CSRFToken`、`Origin` 原样传递这些值。写请求的 Origin 必须与部署公开地址一致；不要改写方法或代填 Origin。

```go
ctx = artifactv1.WithRequestIdentity(ctx, artifactv1.RequestIdentity{
    Session:     sessionCookie,
    CSRFToken:   csrfToken,
    Origin:      origin,
    TenantID:    tenantID,
    ProjectID:   &projectID, // artifactv1 用 *string；storagev1 用 string
})
```

**自定义 resolver 接入**：部署方实现 `service.IdentityResolver`，自行校验 `BearerToken` 等凭据并返回 `RequestIdentity`（`UserID`、`TenantID`、`ProjectID`、`ResourceUserID`、`OwnershipState`）。SDK 不参与认证。

其他部署要点：

- 租户 bucket 由可信租户系统用独立服务身份调用 `CreateTenantBucket` 创建；该入口走 `TenantServiceVerifier`，用户 Header 不赋予管理权限。bucket 未准备好时文件登记会失败，纯 JSON 业务仍可用。
- 引入 grants 需要配置 `Storage.GrantIssuer`（独立普通 IAM 凭据 + STS 端点），不能复用 bucket 管理凭据。
- `StorageConsumerID` 固定文件引用的持久归属，一经部署不得更改，否则无法查询和释放已有引用。
- 存储与 artifact 使用独立数据库连接和事务；Worker 随服务启动和停止。

## 15. 参考

- [OpenAPI（Swagger 2.0）](openapi.json)：由 `make doc` 从 Controller 注解和 SDK 类型生成，字段级定义以此为准。
- 端到端调用顺序的真实例：`tests/integration/file_delivery_live_test.go`。
- SDK 线格式测试：`tests/sdk/`。
- 可选用户归属与幂等创建契约：[service-consumer-design-20261016.md](service-consumer-design-20261016.md)。
- Forge session 接入设计：[tenant-integration-design-20261016.md](tenant-integration-design-20261016.md)。
- 部署与验收：[deploy/forge/README.md](../deploy/forge/README.md)、[部署记录](orbstack-rebuild-acceptance-20261020.md)。
