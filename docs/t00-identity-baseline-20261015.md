# T00：工作副本与身份接入核对

日期：2026-10-15。状态：调查及既有测试完成，以下实施建议已获用户批准；下文实测记录仍是T00调查时点，Git初始化与T01进展见实施计划。

## 工作副本

| 核对项 | 实测结果 | 实施建议 |
| --- | --- | --- |
| 当前目录 | `/Users/light/Code/artifact-go` 没有 `.git`，`git rev-parse --show-toplevel` 失败；没有 `go.mod` | 保留现有目录和设计资料，在正式实施时原地初始化仓库，不迁移或覆盖资料 |
| GitHub 仓库 | `gh api repos/FlyTOmeLight/artifact-go` 返回公开仓库、默认分支名 `main`；`gh api repos/FlyTOmeLight/artifact-go/git/trees/main` 返回 HTTP 409 `Git Repository is empty.` | 远端没有可拉取的代码提交；第一份提交应包含 review 后设计、Spec 关联和必要部署/探针资料，无需设计旧代码迁移 |
| 本地临时资料 | 目录包含 `.scratch/`、`.codegraph/`、`.ruff_cache/` | 首次纳入版本管理前明确忽略规则，只加入有意维护的文件；旧 ticket 草稿不能作为已批准实施任务 |

本轮没有执行 `git init`、clone、commit 或 push。GitHub Issue 中存在规格不代表仓库已有 Git 提交。

## 身份接入的现有能力

实际参考仓库是 `/Users/light/Code/forge-tenant-api`；`/Users/light/forge-tenant-api` 是指向它的符号链接。已读取项目规则和 `docs/project-context.md`。其 HEAD 为 `bdb47baa137cd1dd38e70221c4ec87b90351f528`，工作区有 90 条修改/未跟踪状态，以下源码证据不能视作已发布或已部署能力。

1. `service/router.go:13` 用 server-go 的 `auth.BearerTokens` 验证配置中的 `ServiceToken`，当前所有持有该 token 的调用方都映射为 `forge-service`。
2. `sdk/go/tenantv1/tenant_api_session.go` 与 `internal/app/authentication/session_check.go` 提供 `POST /internal/v1/session/check`。输入是用户 session、原始 method、origin、csrf；写请求校验 Origin/CSRF，输出用户 ID、姓名、邮箱及 operator。它不返回 TenantID、ProjectID 或归属生命周期。
3. `internal/app/workspace/integration.go:12` 的 workspace/check 只接受当前 active 用户、租户和成员资格，并返回可访问项目。`internal/app/authorization/integration.go:10` 的 access/check 按 read/write/manage 判断项目权限。
4. 这些契约没有提供可直接满足 artifact `IdentityResolver` 的“已验证用户 + 精确 scope + active/deleted 归属状态”完整结果。尤其不能将 workspace 的 403 当成 deleted；它也可能表示撤销成员资格、停用或跨租户访问。

## 推荐接法

### 用户请求

保留 `IdentityResolver(*http.Request)` 注入边界。适配器归部署接入层：先用租户系统验证用户，再由租户系统验证请求选择的精确 TenantID/ProjectID，最后转换为 artifact 的可信身份。Header 只能表达选择，不能直接授予身份。角色与项目代码权限留在租户侧，不进入 artifact 领域类型。

已有浏览器路径可复用 session/check，但需要保留原始 method/origin/csrf，不能统一伪装为 GET。当前源码没有证明 artifact SDK 所需用户 Bearer token 可直接由这个接口验证；不要把服务 ServiceToken 用作用户 token。

历史归属读取需要接入层提供明确的“该用户仍有权读取这个已删除 scope”的结果。建议租户系统提供统一 scope 解析契约，返回用户、精确 scope 及归属状态；是否允许该用户访问由租户系统负责。新增契约及测试是实际接入工作，当前没有实现或验证。不能用本地历史 artifact 记录推定当前用户仍被授权。

T01 可使用显式注入的受控 resolver 验证业务行为；独立启动配置缺少生产 resolver 时启动失败。真实身份联调应在 T12 前完成，不能因受控测试通过就宣布生产身份接入完成。

### 服务交接与租户管理

复用既有 HTTP Bearer 认证机制，无需另造 JWT。为 artifact→storage 的引用交接、tenant→storage 的 bucket 管理配置不同 Secret 和服务名。认证结果映射为稳定 ConsumerID（例如 `artifact`），多副本、重部署及 token 轮换保持相同 ConsumerID；不得接受普通请求自行填写 ConsumerID。

storage 为这个服务身份配置可服务 scope，按请求中的精确 TenantID/ProjectID 校验。初期人工管理明确的 scope allowlist，新增租户/项目时同步配置；单次请求不能混合 scope。若未来接入集中服务身份系统，保留相同 ConsumerID 和引用记录即可。当前租户仓库的单一 `forge-service` token 不能直接承担服务之间的隔离，不应复用为上述两个凭证。

生产或跨主机连接使用 HTTPS；同机回环开发连接可使用 HTTP。鉴权 Secret 仅由部署环境注入，不记录在 Spec、测试报告或命令参数中。当前没有为 artifact 创建或分发这些 Secret，也没有真实 artifact→storage 身份联调。

## 本轮验证

| 检查 | 结果 | 证明范围 |
| --- | --- | --- |
| GitHub metadata + tree 查询 | metadata 成功；tree 409 空仓库 | 远端尚无提交基线 |
| `GOWORK=off go test ./sdk/go/tenantv1`，工作目录 forge-tenant-api | 通过，工具汇总 17 个测试 | 现有 SDK HTTP 请求路径、Bearer 传递、响应/错误、取消等本地测试；不代表真实登录成功 |
| `GOWORK=off go test ./service -run '^TestFrameworkBindingAndServiceAuthentication$' -count=1` | 通过，1 个父测试及 6 个子测试 | 缺失/裸/错误 Bearer 被拒绝，未知 JSON 字段、多文档及超限请求被拒绝 |
| 实际用户与历史 scope 接入 | 未验收 | 缺少满足完整 resolver 契约的适配器、用户 Bearer 验证契约及历史 scope 授权结果 |
| artifact/tenant 独立服务身份 | 未部署 | 已有认证机制可复用；凭证区分、稳定 ConsumerID、scope allowlist 仍需正式实施及联调 |

测试仅执行既有 SDK 和无数据库的鉴权测试；本轮未运行 PostgreSQL 成员生命周期测试，未登录 Casdoor、修改租户系统、读取私有配置或输出凭证。CodeGraph 已优先查询；对其未返回的具体 session/router 文件使用源码读取补齐。
