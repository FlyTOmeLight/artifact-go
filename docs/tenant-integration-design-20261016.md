# Forge session 与物料服务接入

日期：2026-10-16。状态：已核实当前源码，两个 SDK 的逐请求 session 传递与隔离已实现并通过定向检查；scope 准入和历史访问范围已由用户确认。本页中的新接口和配置为接入方案，不表示已经部署或验收。

## 接入结果与边界

沿用 Forge 登录，不新增用户 token 或登录页。浏览器从现有同源入口访问 artifact/storage；租户服务验证用户 session 与请求选择的精确 scope，接入层将结果转换为现有 `service.RequestIdentity`。artifact 继续只处理集合、版本、草稿、引用及其状态，不接入 read/write/manage、角色或项目 grants 操作权限。

用户 session、artifact 调用 tenant 的服务凭据、tenant 调用 storage 的管理凭据、artifact 调用 storage 的引用交接凭据分别配置，不互相替代。现有 `IdentityResolver` / `TenantServiceVerifier` 注入接口保持不变。

## 已核实的客户端与服务能力

- `forge-tenant-api/internal/app/authentication/controller.go` 设置 host-only、Path=/、HttpOnly、SameSite=Lax 的 `forge_session`；HTTPS 时设置 Secure。前端不会通过脚本读取 session。
- `forge-tenant-web/src/api.ts` 使用 same-origin 凭据，写请求携带 `X-CSRF-Token`；其来源为 `/api/v1/me`，在前端内存保存。
- `forge-coding-api/internal/app/access/middleware.go` 已有 session 转发先例：将原始 Method、Origin、CSRF 与 Cookie 交给 tenant。不能把写请求改成 GET，也不能由接入层代填一个被允许的 Origin。
- tenant `CheckSession` 校验会话期限和 active 用户；写请求要求原 Origin 等于 PublicURL，CSRF 与该 session 匹配。
- `CheckWorkspace` 混合租户成员准入和 grants 过滤；`ResolveProject` 要求项目已有 GitLabID。它们均不能原样充当 artifact 的精确 scope 解析接口。
- tenant 当前没有租户/项目删除模型或历史归属回执，不能把 403、404、成员移除、租户 suspended 或网络故障转换成 `OwnershipDeleted`。

## 已确认的两项范围

1. 进入 scope 必须是 active 租户成员。准入检查由 tenant 执行，项目只核对精确归属，不查询 read/write/manage 或 grants；平台 operator 也不能绕过租户成员资格。
2. 本轮只接 active scope。租户 suspended、归属不存在、成员失效均拒绝；没有权威删除记录及历史访问规则时，不返回 deleted，也不新增租户/项目删除功能。

## 最小内部接口

建议 tenant 新增 `POST /internal/v1/scope/resolve`，沿用已有 internal 服务 Bearer。原始调用身份只能由 session 解析，不接收调用方自报 UserID、角色、管理员标志或 ownershipState。

```json
{
  "session": "<original forge_session>",
  "method": "POST",
  "origin": "<original Origin>",
  "csrf": "<original X-CSRF-Token>",
  "tenant_id": "t_example",
  "project_id": null
}
```

成功返回 tenant 现有 API 的直接 JSON 对象：

```json
{
  "user_id": "u_example",
  "tenant_id": "t_example",
  "project_id": null,
  "ownership_state": "active"
}
```

省略或 null 的 project_id 只选择租户直属范围；空字符串拒绝。非空必须核对 `projects.id` 与 `projects.tenant_id`，不能依赖 GitLab 是否同步成功或从 grants 过滤清单推定项目存在。tenant_id 必填；method 必须显式传入，不能默认 GET。

服务执行顺序：校验输入 → 现有 CheckSession → tenant 状态 → active 租户成员资格 → 可选精确项目归属 → 返回可信结果。不查询项目代码权限，不采用 operator 绕过准入。数据库故障与业务不存在分开。

建议沿用既有错误包与 wire envelope：用户会话无效为 401 AUTH_REQUIRED，CSRF 失败为 403 CSRF_REJECTED，scope 输入无效为 422 INVALID_SCOPE，不存在或不匹配为 404 SCOPE_NOT_FOUND，租户 suspended 为 403 SCOPE_INACTIVE；无有效成员资格为 403 SCOPE_ACCESS_DENIED。服务 Bearer 错误仍是 SERVICE_AUTH_REQUIRED。数据库/网络失败不能伪造 active/deleted。

## 逐请求凭据与装配

两个 Go SDK 的 RequestIdentity 增加 Session、CSRFToken、Origin；每次请求分别写 `forge_session` Cookie、`X-CSRF-Token` 与 `Origin`，与已有 TenantID/ProjectID 选择头一起传递。BearerToken 保留给现有自定义身份接入及服务管理调用，不解释为 Forge session。共享 Client 不保存用户凭据；并发请求之间不串 session、scope 或 Origin。

Forge 适配器归接入模块：读取 Cookie 和原始请求属性 → 调 tenant scope/resolve → 校验响应用户、scope 与请求精确一致 → 转为 RequestIdentity。服务凭据错误、传输失败、响应缺失或 scope 不匹配返回身份服务不可用；仅明确的用户认证拒绝返回用户身份错误。不缓存成功身份跨请求，也不把上游 403 转成 deleted。

`cmd/forge-server` 可选 Forge 入口调用现有 `BuildWithIdentity` / `BuildWithVerifiers`；不在 artifact 领域 service 中硬编码 tenant HTTP 调用。tenantv1 已增加 ResolveScope，但当前工作区未发布且最低 Go 版本 1.27.1 高于 artifact 1.27.0。因此 integration/forgetenant 对这个单一受认证 HTTP/JSON 接口进行传输适配，不引入未发布 Go 模块或永久 replace，也不提高核心服务工具链要求。它调用真实 tenant 身份服务，不另造登录体系。

浏览器在现有同源代理新增独立 `/artifact/`、`/storage/` 路径并去掉前缀转发到两域公开 HTTP 路由；tenant 原有 `/api/`、`/auth/`、`/internal/` 行为保持。原 Cookie、Origin 和 CSRF 原样转发，内部 scope/resolve 不公开。配置变更属于部署接入，不扩大 Cookie Domain，也不启用跨域凭据回退。

## 租户开通 bucket 与失败处理

tenant 的创建事务提交本地租户及首位管理员，并持久记录 bucket 开通义务；worker 在事务外使用独立管理凭据调用 `POST /v1/storage/tenant-buckets`，相同 TenantID 重放可确认同一个 bucket。存储不可用不回滚已创建租户，也不允许提交本地事务后丢失开通义务。开通完成前纯 JSON 业务可用，文件操作明确报告 bucket 未准备好。

tenant 现有 operations 是 GitLab 同步状态，不是可直接复用的通用 outbox：tenantwrite.Enqueue 会按租户新版本 supersede 旧操作，GitLab 同步成功后统一确认 succeeded，GitLab 失败又会提前中止。bucket 开通若挂在同一状态上，可能被后续版本覆盖，或被无关 GitLab 故障阻塞。

采用独立的 tenant_bucket_provisions 持久任务，tenant_id 唯一；在 CreateTenant 同一事务登记义务，单独保存状态、尝试次数、下次执行时间、租约、错误和已确认 bucket 结果。专用 worker 可复用现有进程生命周期和数据库连接，不共用 operations 的版本替代或完成回执。需明确有限失败预算及受控重排，再由 worker 在事务外调用 storage；不新增通用任务引擎。

调用 endpoint、管理凭据和超时归部署配置，凭据不进任务载荷。现有租户回填按 tenant_id 幂等登记，不能只在新建 HTTP 请求中临时调用一次。依据：forge-tenant-api/internal/tenantwrite 中的 Enqueue、internal/table/operations.go 的 SupersedeOperations，以及同步 worker 的完成/失败路径。

## 修改位置与验证

| 仓库 | 修改范围 | 观察结果 |
| --- | --- | --- |
| forge-tenant-api | scope/resolve 业务与路由、精确 scope 查询、tenantv1 类型/方法及 PostgreSQL 集成用例 | 真实 session 校验、输入/CSRF/状态/项目归属与准入规则正确；缺库跳过不算通过 |
| artifact-go | 两个 SDK 的逐请求身份及专用测试；后续 Forge 接入模块与入口 | Cookie/CSRF/Origin 不串请求；真实 tenant 响应变为可信 scope；默认无身份配置仍拒绝启动 |
| forge-tenant-api | tenant_bucket_provisions 表与查询、创建事务登记、专用 worker、管理 SDK 调用与配置 | 创建后崩溃/重启、重复投递、远端失败不会丢失义务或重复建空间 |
| forge-tenant-web | 同源代理配置、实际 artifact/storage 调用与浏览器验收 | 登录一次即可使用物料链路，写请求仍受 CSRF 保护 |

集成验证使用独占数据库及隔离 JuiceFS，不修改已有业务数据。至少覆盖无 session、过期/停用用户、错误 CSRF/Origin、跨 tenant 项目、suspended、用户确认的成员准入、身份服务中断、请求结果重放仍先校验身份，以及真实登录后的上传/草稿/发布/下载/删除。不得以自报 Header 或固定用户 resolver 代替真实身份验收。

四层联合恢复已作为独立本机验收完成：同一冻结点保存两业务库、JuiceFS 元数据、对象及本轮测试配置，恢复到全新资源后读取原版本并继续处理持久义务。实际结果见[恢复记录](four-layer-recovery-20261016.md)，生产恢复设施与密钥管理仍由最终部署环境确定。

## 本轮已完成与检查

已修改两个 SDK 的 RequestIdentity 与请求构造，支持逐请求 Session/CSRFToken/Origin，禁用共享 CookieJar，并明确拒绝 HTTP 重定向。原 Bearer 管理调用继续可用。独立审查真实复现了响应 Set-Cookie 污染后续无身份调用，以及 307 跳转转发 CSRF 的问题；修正后测试验证换用户、服务身份、空身份均不继承 Cookie，跳转目标不收到请求。

`go test -race -count=1 ./tests/sdk ./sdk/go/artifactv1 ./sdk/go/storagev1` 通过，共 43 项；同范围 vet 与四个改动 Go 文件的 gorgeous 检查通过。以上为前一轮 SDK 检查记录。随后本轮已实现 tenant scope 接口、Forge 接入入口与 bucket 开通义务，实际验收结果另见本轮交付记录；未自动提交、推送或部署。
