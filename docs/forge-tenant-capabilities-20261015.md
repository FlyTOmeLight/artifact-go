# forge-tenant-api 身份接入能力核实

核对日期：2026-10-15。范围：只读核实 `forge-tenant-api` 当前工作区及本机部署元数据；未修改该仓库，未调用生产写接口，未读取或输出密钥。

结论：当前租户 API 已有用户会话校验、精确租户成员检查、精确项目权限查询及 Go SDK，可以支持 active scope 的接入基础。完整满足 artifact-go `IdentityResolver` 的历史归属授权、面向用户 Bearer 的认证契约，以及租户创建后调用 storage 开通 bucket 的链路尚未具备，不能直接把现有 SDK 插入就宣布身份接入完成。

## 实现与部署范围

- 当前源码 HEAD：`bdb47baa137cd1dd38e70221c4ec87b90351f528`；核对前后均有 90 条修改或未跟踪状态。以下源码能力包括未提交工作区，不能视为发布版能力。
- 已读取租户仓库 `AGENTS.md`、`docs/project-context.md`。项目背景仍明确记录大量重构未提交、未部署，以及干净 MVP 与历史迁移路径之间的待处理范围。
- 本机 Helm 元数据实测后端 chart/app_version 为 `0.8.1`，前端 chart/app_version 为 `0.2.0`，release 状态均为 deployed。这是本机联调环境元数据，不是生产环境验收，也不证明当前工作区已部署。
- 优先执行 CodeGraph 查询，但报 `unable to open database file`，随后按规则直接核对源码，没有修复或重建索引。

## 可复用能力

以下源码路径均相对 `forge-tenant-api` 仓库根目录。

| 能力 | 当前实际行为 | 证据 |
| --- | --- | --- |
| 浏览器登录 | OIDC 授权流程使用 state、nonce、PKCE；仅已激活 Forge 账号可建立本地会话 | `internal/app/authentication/login.go:18-72` |
| 用户会话校验 | `POST /internal/v1/session/check` 接受 session、原始 method、origin、csrf；非 GET/HEAD 校验 PublicURL 与 CSRF，返回用户 ID、姓名、邮箱、operator | `internal/app/authentication/router.go:12`；`internal/app/authentication/session_check.go:11-24`；`sdk/go/tenantv1/tenant_api_session.go:3-15` |
| 会话有效性 | 用 session 哈希查数据库，仅会话未过期且用户 active 才有效；会话有效期 8 小时 | `internal/app/authentication/sessions.go:13-30`；`internal/table/users.go:101-107`；`internal/table/sessions.go:20-29` |
| 精确租户成员检查 | `POST /internal/v1/workspace/check` 按给定 UserID、TenantID 查询 active 用户、租户和成员；不成立返回 403；成功返回可访问项目 | `internal/app/workspace/controller.go:24-26`；`internal/app/workspace/integration.go:12-44`；`internal/table/memberships.go:41-44` |
| 精确项目授权 | `POST /internal/v1/access/check` 按 UserID、TenantID、ProjectID 检查 read/write/manage，返回 allowed、level、授权版本、精确 tenant/project；无授权返回成功查询结果 allowed=false、NO_GRANT | `internal/app/authorization/router.go:12`；`internal/app/authorization/integration.go:10-29`；`internal/app/authorization/permissions.go:147-169`；`internal/table/grants.go:103-115` |
| 权限收敛 | 实际权限取 desired/observed/effective 最低值；任一用户、成员、租户非 active 则降为 0；read/write/manage 阈值为 20/30/40 | `internal/app/authorization/permissions.go:156-168` |
| 服务调用认证 | 所有 `/internal/v1` 路由通过配置中的单个 ServiceToken 校验 Bearer，统一映射为 `forge-service` | `service/router.go:8-24` |
| Go SDK | 已有 CheckSession、CheckWorkspace、ResolveProject、CheckAccess、ListAccessEvents；逐调用 context，默认 15 秒超时，拒绝重定向，保留业务错误及 HTTP 状态 | `sdk/go/tenantv1/README.md`；`sdk/go/tenantv1/tenant_client.go:18-67,79-103` |

身份必须来自 `CheckSession` 的成功结果，不能直接信任请求中自报的 UserID。`CheckWorkspace.CanEdit` 与 `CheckAccess` 的 read/write/manage 是租户系统现有能力；artifact 首期不消费这些字段判定操作权限，也不建立角色或 grants 授权适配器。首期范围已由本仓库 `docs/version-service-technical-design-20261013.md:94` 确定：接入方验证可信用户、精确 scope 及 active/deleted 归属后，scope 内操作遵循 artifact 自身的资源状态、保护、引用和并发条件。

## 尚缺的接入契约

1. **历史与已删除 scope 授权。** tenant 状态模型只有 active/suspended（`internal/table/tenants.go:11-19`）；project 模型没有生命周期字段（`internal/table/projects.go:9-17`）。租户路由只提供创建、状态更新和查询（`internal/app/tenant/controller.go:37-42`），项目路由只提供接入、查询和授权操作（`internal/app/project/controller.go:22-33`），未发现 tenant/project 删除及历史归属解析接口。workspace 的 403、access 的 NO_GRANT 或接口不可达均不能转换成 artifact 的 `OwnershipDeleted`。
2. **用户 Bearer 认证。** 当前浏览器入口读取 `forge_session` Cookie（`internal/httpapi/response.go:102-120`）；session/check 验证的是 Forge session。未发现面向外部用户 access token、API key 或任意 Casdoor JWT 的通用验证接口。SDK 的 ServiceToken 是服务凭证，不能作为用户身份。若正式客户端继续使用用户 Bearer，需要明确其发行和验证契约；选择现有 session 路径则必须转发原始方法、Origin 和 CSRF。
3. **tenant → storage 管理身份与开通动作。** 对 `internal/`、`service/`、`sdk/` 和 `go.mod` 搜索未发现 artifact/storagev1/bucket 集成。租户创建目前在事务内插入 tenant 与首位管理员并返回结果（`internal/app/tenant/service.go:33-68`），没有调用 storage。现有入站 ServiceToken 只证明调用者有权访问 tenant internal API；它不能自动证明某个入站 artifact 请求来自租户系统。需要为 tenant → storage 配置独立凭证、在 artifact 部署接入层实现 `TenantServiceVerifier`，并在租户侧落实 bucket 开通调用及失败处理。

## 建议的下一步

先确定 artifact 用户凭证使用现有 Forge session 还是独立用户 token。CheckSession 可提供可信用户，workspace/project 现有接口可作为核实当前归属关系的参考，但不应把它们的角色和 grants 判定引入首期操作权限。租户侧应补充或明确可信 scope 解析契约，返回精确 TenantID、可选 ProjectID 及 active/deleted 归属，并明确历史归属可由谁读取，以及 suspended、removed、deleted 的不同处理。

随后为 tenant → storage 单独配置服务凭证与调用身份，补齐租户开通 bucket 的真实调用。普通用户身份、artifact → storage ConsumerID 凭证、tenant → storage 管理凭证必须分别装配。上述契约落实后，再实现本仓库的 resolver/verifier 并做真实登录及跨服务验收。

## 本轮实际验证

| 检查 | 结果与证明范围 |
| --- | --- |
| `GOWORK=off GOCACHE=/private/tmp/forge-tenant-capabilities-gocache go test ./service -run 'TestFrameworkBindingAndServiceAuthentication' -count=1` | 实际使用包含同名目标的联合命令运行；1 个父测试、6 个子测试通过。验证缺失/裸/错误 Bearer 被拒绝及请求绑定约束；不依赖数据库 |
| `GOWORK=off GOCACHE=/private/tmp/forge-tenant-capabilities-gocache go test ./sdk/go/tenantv1 -count=1` | 17 个测试通过。首次受沙箱限制无法监听临时端口，获执行权限后原命令通过；测试使用本机 httptest，不是实际 tenant 登录联调 |
| Helm release 元数据 | 本机后端仍报告 0.8.1；未调用已部署业务接口，因此未验证运行镜像与当前源码等价 |
| PostgreSQL 授权链 | 本轮未运行，未提供真实测试数据库；现有 `service/sdk_test.go:12-100` 有真实 PostgreSQL SDK/撤权联调用例，`service/tenant_test.go:52-56` 明确缺 TEST_DATABASE_URL 时跳过 |
| Casdoor/GitLab 与 artifact 实际联调 | 本轮未执行，不能据上述单元测试宣布完成 |

本轮只新增本报告，没有修改 forge-tenant-api 工作区，没有创建、提交或部署身份适配器。
