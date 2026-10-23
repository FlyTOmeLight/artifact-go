# Forge 全新 Helm 安装记录（2026-10-20）

目标：用一份 Helm chart 在全新 namespace `forge` 安装 tenant、web、artifact 与 JuiceFS，自动生成内部凭据、证书、数据库和初始化，随后完成真实登录与存储验收。已退役的独立应用与共享存储环境的清理不在 chart 生命周期内。

**当前状态（2026-10-22）：OrbStack 的 `forge` namespace 已升级到 release `forge` revision 9，Artifact、Tenant 和 Web 均运行 `forge-review-20261022` 镜像，全部常驻容器 Ready，外部 bootstrap Job 完成。升级后真实 Casdoor 登录、四类资源范围的版本与 STS 字节回读、Artifact 重启和已删除样例复查通过；`forge-redeploy.cjs --verify --restart` 于 `11:53:27.767Z` 返回 `{"status":"passed"}`，退出码 0。完整写入、跨范围拒绝、写授权保护及引用释放证据见此前同日报告；本次复验未新建过期删除样例。已退役的独立环境清理见下文。**

## 范围

- 新 release `forge`，namespace `forge`；不复制旧 Secret，不迁移旧 artifact/storage 元数据。
- 本轮关闭 Coding 与 Agentic；web 隐藏相关导航与路由，代理对关闭功能返回明确 404。
- 继续连接 `tenant-lab` 的 Casdoor/GitLab，注册新的 Forge 专用 OIDC 应用、管理客户端、初始用户和 GitLab PAT。不修改共享 GitLab OmniAuth 回调，不重启 `tenant-lab`。
- 外部管理员授权与 issuer CA 为必填输入；不读取旧 `.local`、旧 Secret 或本地备份中的权限。

## 实现

| 位置 | 内容 |
| --- | --- |
| `charts/forge` | umbrella chart、公开 values/schema、seed 与派生 Secret、内部 CA/证书、外部 bootstrap Job、渲染行为测试 |
| `charts/artifact` | fullname、Forge 配置/Secret 生成、`artifact-migrate` initContainer、storage IAM init、PostgreSQL TCP 就绪检查 |
| `charts/juicefs` | 单 PVC 的 Redis AOF + JuiceFS gateway，空元数据初始化，已有元数据校验后不重置 |
| `cmd/migrate` | 只解析 DB/StorageDB，复用两域迁移函数 |
| `cmd/storage-bootstrap` | 独立 module/镜像，复用 madmin-go 创建普通 STS issuer 与最小 policy |
| forge-tenant-api chart/CLI | fullname、`wait-external`、`service-credential ensure`、外部 bootstrap 命令与单 Secret 受限写入 |
| forge-tenant-web | fullname、可选 Coding/Agentic、公开 runtime 开关 |

内部密码、服务 key、TLS 和 JuiceFS 凭据由 Helm 一次生成并保留；升级复用，不重新签发。GitLab PAT 只能由 GitLab 返回一次，由普通 bootstrap Job 写入预创建的单个输出 Secret；该 Job 的 Role 仅允许对该 Secret 执行 get/patch。业务 Pod 不挂载管理员授权。

## 已执行检查

| 检查 | 结果 |
| --- | --- |
| `helm dependency build` / `helm package` | 通过，生成含四个子 chart 的 `forge-0.1.0.tgz` |
| 总 chart `helm lint --strict` | 通过；仅有 chart icon 建议 |
| 独立包渲染 | 包移出依赖路径后渲染 35 个资源，无重复资源名，无旧 namespace 引用 |
| `go test ./charts/forge` | 通过：多 release/namespace 渲染、引用解析、seed 派生、`ftk_` 格式、TLS SAN/私钥匹配、Role 范围、关闭 Coding/Agentic、初始化顺序；另有一组 release/namespace 名称边界用例，验证会截断或超过 63 字符的组合在渲染入口直接失败 |
| `go test ./cmd/migrate`、`go vet` | 通过；真实临时 PostgreSQL 首次与重复双库迁移通过（子代理执行）。后续补充 MySQL 握手超时：`db.NewDB` 没有注入 Dialector 的入口，GORM 的 MySQL 初始化用 `context.Background()` 执行 `SELECT VERSION()`，`--timeout` 无法直接取消它。改为按剩余 deadline 在 DSN 上设置 `timeout`/`readTimeout`/`writeTimeout`，已配置的更短值保留；本地不发握手应答的 TCP 回归覆盖了 flag 超时、更短调用方 deadline 和取消三种情况（9 项通过，`go test -race`） |
| web | `npm run build`、格式检查、feature/导航测试、Helm 渲染通过；关闭路由实测返回 404 JSON；镜像 `forge-tenant-web:forge-runtime-20261020`，ID `sha256:639660c3ca5f78940c614f92583f6feb0879d6c560ccf3530a74013d56f7737e` |
| `helm install --dry-run=server --hide-secret` | 通过；使用虚构 CA 与 existingSecret，未安装、未创建 namespace 资源 |
| tenant 外部初始化 | 官方 API 协议模拟 9 项通过；`go test ./...` 266 项、真实 PostgreSQL service credential 7 项、`go vet` 通过 |
| storage-bootstrap | 独立镜像构建通过；隔离 JuiceFS 首次/重复 ensure、错误凭据/归属/policy 拒绝、网关重启后复用、普通 STS issuer、跨 key 拒绝及二进制字节一致全部通过 |
| 镜像 | `artifact-go:helm-init-20261020`、`forge-tenant-api:helm-init-20261020`、`artifact-go-storage-bootstrap:helm-init-20261020`、`forge-tenant-web:forge-runtime-20261020` 均已在本地构建 |

渲染和 server dry-run 只能证明清单可被接受，不代表 Pod 能启动或初始化成功。外部 Casdoor/GitLab 注册已在 2026-10-21 的真实安装中执行并通过 bootstrap Job。

## 实际安装（2026-10-21，OrbStack）

| 项目 | 结果 |
| --- | --- |
| Helm release | 安装为 revision 7，补齐配置后升级到 revision 8，状态 `deployed`；安装和升级均带 `--wait --wait-for-jobs` |
| Pod | artifact、artifact-postgres、juicefs、tenant-api、tenant-worker、tenant-postgres、web 均 Running/Ready |
| PVC | tenant-postgres、artifact-data、juicefs 三个 PVC 均 Bound（local-path） |
| 外部 bootstrap | Job 完成，输出 Secret 含 `BOOTSTRAP_COMPLETE=true`、Casdoor 应用/用户与 GitLab token 字段 |
| Superseded Job | revision 5 的 3 个失败 Pod 随升级已消失；当前只有 revision 8 的完成 Pod |
| 服务健康 | web `http://127.0.0.1:8483/` 返回 200，未登录 `/api/v1/me` 返回 401；tenant `/healthz` 返回 ok；Artifact 19010 S3 代理对匿名请求返回 403 AccessDenied |
| 镜像 | `artifact-go:helm-init-20261021`、`forge-tenant-api:forge-bootstrap-20261021b`、`forge-tenant-web:forge-runtime-20261020` |

升级到 revision 8 时，chart 渲染的 Artifact 配置补上了显式的 `StorageFiles`、`StorageWorker`、`StorageGrants` 三段。`StorageFiles` 与 `StorageWorker` 的取值和 `service/storage_config.go` 的内置默认值完全相同，写出来只是让配置可见，行为没有变化。真正影响验收时长的是 `StorageGrants`：chart 把 `UploadTTL`/`ReadTTL` 设为 16 分钟，内置默认是 1 小时。写入授权到期时间记录在 `storage_files.last_grant_expires_at`（只有写授权会更新该字段），删除任务在它到期前把 `next_run_at` 推后，因此授权时长直接决定文件进入 `deleted` 前的等待时间。

## 真实登录与资源验收（2026-10-21/22）

| 检查 | 结果 |
| --- | --- |
| 登录链路 | 8483 指向 `forge/forge-web` 后，`/auth/login` → `/login/oauth/authorize` → `/api/login` 200 → `/auth/callback` 303 → `CompleteLogin` 按 issuer + subject 命中 `users`，取得 `u_7e2b38d33d6c5253496795d3` |
| 四类范围 | tenant-shared、tenant-personal、project-shared、project-personal 各自完成登记文件、STS PUT、Complete、创建 artifact、创建版本、Resolve 与 STS GET 原字节校验 |
| 跨范围拒绝 | storage 文件、complete、grant、delete 返回 404；artifact、版本、草稿、alias、resolve 返回 404；retention-policy 返回 405 |
| 幂等与冲突 | requestKey 重放返回同一结果，改内容后返回 409；同名 artifact 返回 409 |
| 重启回读 | `rollout restart` Artifact 后，四个 scope 的版本和 Resolve 均可读，文件字节一致 |
| 引用释放 | 删除共享版本返回 200 并进入 `deleting`，随后观察文件保持 `bound`，剩余版本和文件字节仍可读；旧版未通过版本完成回执确认第一个释放任务已执行。删除最终版本返回 200 并进入 `deleting` |
| 物理删除回读 | 原写授权到期时间为 16:02:13Z；16:34:08Z 是读授权到期时间，读取授权不更新 `last_grant_expires_at`，不延缓回收。16:38:00.123Z 回读观测到 `deleted`，该时间不代表实际进入 `deleted` 的时刻。原版验收脚本输出 `{"status":"passed"}`；此结果不证明后来新增的断言已通过 |
| 其余拒绝路径 | 无效 CSRF、非法 Origin 与匿名请求均被拒绝；脚本最终返回 0 |

镜像 `forge-tenant-api:forge-bootstrap-20261021b` 修正了登录查询的错误映射：`ActiveUserBySubject` 只有 `sql.ErrNoRows` 才返回 `IDENTITY_NOT_ENROLLED`，其他数据库错误按内部错误处理，不再全部伪装成 403。

验收脚本按真实 API 行为修正了断言：Resolve 只返回 `version`（不含 `file`），storage 文件 DELETE 对未绑定文件返回 202，版本 DELETE 返回 200 且 `state=deleting`，跨 scope 的 retention-policy 为 405。

脚本后来改为按服务端回执推进，不再推测等待时间。引用释放的进度由版本 DELETE 回执确认：同一版本重复 DELETE，直到返回 `state=deleted`，这表示该版本的引用释放任务已经提交；此时末引用版本必须仍为 `ready` 且能取回原始字节。删除末引用版本后轮询存储文件，直到 `deleted`，并断言这个时间点不早于该文件的上传授权到期。每个完成阶段单独写报告（`0600`），`--verify` 从报告记录的服务端状态继续。更早的版本按“两段 grant TTL”估算截止时间，既会误判也会不必要地延长等待，已删除。上表保留原版实测结果；新版真实验收结果见下节。

`forge-login.cjs verify` 的“非成员拒绝”一项未执行：新环境只有一个 active 用户，缺少第二个 active 账号作为负向对照。这是环境条件限制，不是产品行为差异；该路径由 tenant 集成测试覆盖。

安装过程中处理的两个问题：tenant chart 的 Nginx sidecar 需要可写的 `/tmp` 与 `/var/cache/nginx`（已在 artifact chart 补 emptyDir）；外部 bootstrap 记录的 GitLab 管理员 token 已失效且签发计数耗尽，换用新签发的管理员 PAT 并重置 `GITLAB_ISSUE_ATTEMPTS` 后成功。GitLab 19 的 `/user/personal_access_tokens` 只接受 `k8s_proxy`/`self_rotate` scope，管理员替指定用户签发 PAT 走 `/users/:id/personal_access_tokens`。

## 历史问题

登录最初的失败来自本机 8483 的 LaunchAgent：plist 已改指新 namespace，但运行中的进程仍使用旧参数，登录请求全部打到现已退役的独立应用环境，旧库没有新 Casdoor subject，因此返回 `IDENTITY_NOT_ENROLLED`。执行 `launchctl bootout` + `bootstrap` 重新加载后，8483 指向 `forge/forge-web`，真实登录随即通过。`kickstart -k` 只重启进程，不重读 plist 参数，不能用于切换目标。

## 旧环境清理（2026-10-22）

新环境验收通过后，按用户逐项确认的顺序执行：

- 删除现已退役的独立应用与共享存储环境的两个 namespace。删除后两个 namespace 均为 NotFound，无对应 PVC，也无 Released PV；当时集群列出的 10 个 PV 均仍绑定到其他 namespace 的 PVC。清理前 UID 与资源盘点见 `.scratch/forge-orbstack/old-env-20261021/cleanup-inventory-20261022.txt`。
- 撤销 GitLab 遗留的 6 个活跃 `forge-probe-*` PAT（id 11、12、13、15、16、17）。API 均返回 204，复查均为 `revoked=true`。
- `tenant-lab` 的 Casdoor、GitLab、identity、lab-infra 与 verification release 仍在；相关 Pod 保持 Running/Completed。未修改或重启 `tenant-lab`。
- 本机 `~/Library/LaunchAgents/dev.light.forge-web-portforward.plist` 已从旧 namespace 改指 `forge/forge-web`，8483 现在对应新环境。

清理是独立的运维操作，不属于 chart 的 install/upgrade/uninstall 生命周期。退役环境的独立 Artifact values 与旧存储部署脚本已从仓库移除，不再提供历史安装入口；当前环境由 [Forge umbrella chart](../charts/forge/README.md) 自己的 values 渲染。

## 2026-10-22 修复后真实验收

新报告为 `.scratch/forge-orbstack/forge-redeploy-corrected-20261022.json`，使用新建对象，不复用旧版完成标记。所有时间均为 UTC。

- 四类 scope 完成真实登录、STS PUT/Complete/发布/Resolve/GET 字节核对，以及跨 scope、CSRF、Origin、匿名拒绝检查。
- 未绑定删除样例 `f921359a-8a42-4858-96c5-b6662dc4b0e3`：从 `10:20:19.116Z` 开始观察到授权保护状态；写授权于 `10:36:13Z` 到期，轮询中到期前保持 `deleting`，于 `10:36:26.266Z` 观察到 `deleted`。这是轮询证据，不给出精确物理删除时刻。
- 引用释放样例 `e48250c9-19a3-4f84-95f8-f58d3261496a`：删除第一个版本后重启 Artifact，轮询 DELETE 回执直到 `deleted`；于 `10:39:53.788Z` 确认剩余版本仍 ready 且原始字节可读。随后删除末引用，于 `10:40:03.827Z` 观察到文件 `deleted`。
- 首次运行在重启后遇到本地 19010 转发仍连接旧 Pod，STS GET 失败，退出码 1。修复为等待旧 Pod 删除并有界等待匿名 S3 请求返回 403 后，再执行实际 STS 传输；从同一报告继续 `--verify --restart`，于 `10:40:03.847Z` 返回 `passed`，退出码 0。未掩盖失败、未重新创建样例，也未延长写授权。

- 完成后再次运行 `--verify --restart`：于 `10:41:03.895Z` 再次返回 `passed`，退出码 0。四类版本及字节保持一致，两个已删除样例仍为 `deleted`，报告保留首次观察时间。最终工作负载 Ready；web 200、匿名 `/api/v1/me` 401、匿名 S3 403。

## 2026-10-22 修复检查

- `make check` 完整执行，退出码 0；包含格式检查、真实 PostgreSQL/MySQL 集成测试、race、vet、build 和 OpenAPI 一致性。日志：`.scratch/forge-orbstack/check-20261022.log`。
- `go test ./charts/forge -count=1`：16 项通过。`node --test deploy/acceptance/reference-release.test.cjs`：6 项通过，覆盖释放完成回执、剩余字节、重启失败恢复、提前删除拒绝、旧报告证据缺失拒绝、完成报告复查删除状态和轮询超时。
- 两个 JuiceFS Python 探针改为必填 `--credentials-file`，不再读取旧环境固定路径；语法检查及 T08 `--help` 通过。T00 离线 `--help` 因 uv 缓存缺少固定版 boto3 未能执行；未运行这两个会修改 IAM 的独立探针。
- 前端 `npm run build` 通过；四个 vendored 子 chart 与各自当前源码经 Helm 打包后的文件内容一致。
- 本仓旧 Artifact values、三个旧 storage 脚本，以及相邻 tenant API/web 仓库的旧 `values-orbstack.yaml` 均已删除。相关 README 已统一指向 Forge umbrella chart；依赖包与总安装包已重新生成，不再包含这些退役部署输入。

## 最终审查后的修复进度（2026-10-22 升级前记录）

- chart 测试已改为读取真实 `default.conf`，要求非空、Coding/Agent 返回禁用响应且没有对应 `proxy_pass`；16 项渲染测试通过。
- 新 Artifact 镜像 `artifact-go:forge-review-20261022`（`sha256:36739218e056da38364d9822070ca8546c991822b8820accd4ec729a736b703c`）构建完成。在本机隔离假 MySQL 仅接收连接、不回握手的检查中，`--timeout=300ms` 约 0.65 秒退出；旧运行镜像超过 5 秒仍未退出。公开 OrbStack values 已指定新 Artifact 镜像，并把 Tenant 标签对齐到已验收的 `forge-bootstrap-20261021b`。新安装包已生成，尚未升级集群。
- 前端已修复平台连接 query/尾斜杠路由和 Agent 关闭时发布历史证据按钮；当前完整工作区构建通过。AgentWorkbench 关闭 Coding 的修复及针对性回归尚未完成，不能把此次构建当作三项修复均已验收。
- OIDC secret 漂移冲突校验及其测试、AgentWorkbench 修复被自动审批阻止，代码尚未完成；未部署部分修复。部署专用补丁的独立构建验证也尚未完成。
- 两仓根 README 已改指 Forge umbrella 入口，并撤下旧部署与共享 SSO 修改命令。用户逐路径授权后，tenant API 的 `scripts/deploy-lab.sh`、`scripts/prepare-lab.py`、`scripts/configure-lab-sso.py` 和 web 的 `scripts/deploy-lab.sh` 均已删除，实际检查确认四个文件不存在；三仓 `git diff --check` 通过。
- 用户已明确逐路径授权剩余代码与测试修复，但两个原审查子代理的自动审批仍按旧只读边界拒绝 `external_test.go` 和 `AgentWorkbench.vue` 编辑。两代理均停止，没有绕过拒绝；OIDC 校验和剩余前端修复仍未完成，集群未升级。

## 2026-10-22 审查修复完成及 revision 9 验收

前述自动审批阻塞已解除，剩余修复经正常 Edit 审批完成。

- Tenant 的 `ensureApplication` 比较远端与 seed 的 `ClientSecret`；不一致时返回既有脱敏冲突错误，不覆盖远端 secret、不发布完成状态、不更换 GitLab PAT。新增 OIDC/management 两组回归先在修复前失败，再在修复后通过。`internal/bootstrap` 测试、race、vet，以及相邻 external/wait 服务测试与 service vet 通过。
- 前端补齐 `AgentWorkbench` 的 Coding 请求与启用面板开关，以及 `CodingWorkspace.openReleaseReport` 的 Agent 开关。浏览器 fixture 覆盖四种组合、平台连接 query/尾斜杠首屏、历史证据按钮、accessProject 与历史 Coding 报告的请求隔离，并验证开启时仍可进入报告。新增用例在修复前捕获禁用 Coding 仍请求 settings，修复后全部通过；完整 `npm run build`、三文件格式检查及三仓 `git diff --check` 通过。
- `helm dependency build` / `helm package` 完成。首次裸 `helm lint` 因未提供必填输入失败；按现有渲染测试提供公开 values、虚构 CA 和 fixture Secret 后，`helm lint --strict`、16 项 chart 测试和 6 项引用释放测试通过。总包递归检查 39 个文件，无退役 namespace 或子 chart 旧 OrbStack values。
- `helm upgrade --dry-run=server --hide-secret` 通过；仅输出公开镜像标签与输入引用，未输出完整 values 或 Secret。实际升级使用 `--reuse-values -f charts/forge/values-orbstack.yaml --wait --wait-for-jobs`，沿用当前 release 的 `forge-bootstrap-admin` 和 CA 输入，退出码 0。未读取旧备份、未修改共享身份服务。

| 应用 | revision 9 运行镜像 | 已核对的镜像 ID |
| --- | --- | --- |
| Artifact | `artifact-go:forge-review-20261022` | `sha256:36739218e056da38364d9822070ca8546c991822b8820accd4ec729a736b703c` |
| Tenant API / Worker / bootstrap | `forge-tenant-api:forge-review-20261022` | `sha256:6fbde8b4fbd020b7bef226583c938613410a6d84a05b72ce57481ea3ab17a071` |
| Web | `forge-tenant-web:forge-review-20261022` | `sha256:7714c718b435a7516e2ae6f75e19a1dc60a1d3b4fe7fb38ebfd05cbefd5c9561` |

Web 镜像的全部 4 个业务静态文件与通过 fixture 测试的当前 `dist` 一致。该镜像来自完整前端工作区，包含既有未提交功能；独立部署补丁的提交范围验证另行记录，不能将该镜像声称为仅含部署改动。

三个 PVC 的 UID 和绑定 PV 在升级前后相同：Tenant `4e74f95d-00cb-42cf-8bc2-b3aafdc61a43`、Artifact `ac8b324a-d058-4eae-aa55-15b9fe5baa4d`、JuiceFS `a56e541f-41cb-4a87-b473-da6cbd0528f5`。新的 `forge-external-bootstrap-9` 完成，运行镜像 ID 与本机构建一致。部署后 runtime 配置仍为 Coding=false、Agent=false；匿名 me 返回 401，Coding settings 和 evaluations 代理返回 404。

真实复验继续使用显式受限登录配置和 `.scratch/forge-orbstack/forge-redeploy-corrected-20261022.json`，`--verify --restart` 于 `11:53:24.593Z` 等待重启完成，于 `11:53:27.767Z` 返回 passed，退出码 0。日志为 `.scratch/forge-orbstack/forge-review-verify-20261022.log`。四类 scope 字节保持一致，已删除样例仍为 deleted；保留首次保护/删除观察时间。本次未重跑新对象的 16 分钟授权过期周期。验收临时 S3 转发和隔离浏览器已停止，既有 Web LaunchAgent 保留。

### 提交范围

- artifact-go：`charts/artifact` / `charts/forge` / `charts/juicefs`、`cmd/migrate` / `cmd/storage-bootstrap`、对应 Dockerfile、验收脚本与回归、旧输入删除及相关部署文档。既有暂存删除保持原样，未新增暂存操作。
- forge-tenant-api：子 chart 与旧脚本删除、外部 bootstrap CLI / 协议测试、服务 key ensure、登录数据库错误映射，以及部署说明。该仓本次查看到的修改均属于已审查的部署链路，仍应按最终 diff 选择提交。
- forge-tenant-web：chart / runtime 配置 / 启动与功能开关属于部署范围；Agent、Credentials、CodingRelease、相关类型和样式与部署修改混在部分文件中，不能整仓暂存为部署提交。当前运行镜像来自完整工作区，独立部署补丁只用于验证可拆分的提交范围。

前端独立部署补丁已保存为 `.scratch/forge-orbstack/deployment-only.patch`，说明为同目录 `deployment-only-verification.txt`；基线是 `456026fedf4277fabda7879942ecf3ac30ad8cc9`。首轮隔离验证在排除 Agent/Credentials/CodingRelease 组件及静态依赖后完成独立构建（85 modules）、四配置组合浏览器测试和 Helm strict lint。后续审阅发现导出的实际补丁未包含隔离目录的格式化结果，应用后三个文件的 Prettier 检查失败，因此撤回首轮“交付补丁格式通过”的结论；修正后的最终补丁验证见下节。补丁尚未应用到真实仓库或部署；真实 index 未改变。临时源码、构建目录、node_modules 软链与预览进程已清理，仅保留补丁和验证说明。此补丁提供可独立构建的提交范围，不替代当前完整工作区镜像的验收。

本轮启动 2 个子代理，分别完成前端修复回归和部署补丁隔离验证；主线程完成后端修复、镜像/包核对、升级与真实验收。未提交、推送或创建 Git tag。

## 2026-10-22 最终提交范围修正

范围审阅发现的格式、默认 Agent 上游、旧输入和嵌套模块检查遗漏已修正：

- 前端最终 `deployment-only.patch` 基于 `456026fedf4277fabda7879942ecf3ac30ad8cc9`，包含 16 个文件，374 行新增、58 行删除。先格式化再导出，随后在第二份干净基线上应用磁盘上的最终补丁。完整 `npm run format:check`、构建（85 modules）、四配置组合浏览器测试、默认 Helm strict lint 和隔离 Nginx 配置检查通过；README 已不含退役 namespace。最终 SHA-256 为 `b4692366a6c588c1fe04b476baea7bf06a81ddc6a1c1f840ee88d915fc1c8eef`。
- Web chart 的 Coding 默认仍开启，Agent 默认关闭；部署 Agent 上游后再显式开启。真实工作区仅同步该 chart 的 values/README，其他前端功能保留原样。父 chart 四个依赖包与总包重新生成；最终补丁的 Web chart 及另外三个子 chart 均与父包中的内容一致，父 chart lint 和 16 项渲染测试通过。
- Tenant 的 `configs/env.example` 已将旧 OIDC client 改为 `forge-forge-console`。Artifact 交付文档已说明四条二进制和显式 `artifact-migrate` 初始化，移除首次临时开启 AutoMigrate 的旧说明。
- `scripts/check.py` 增加 migrate 显式构建，以及 storage-bootstrap 独立模块的 test/race、vet、build；CI 的模块元数据检查覆盖其 go.mod/go.sum。根模块和独立模块的 `go mod tidy -diff` 均通过且无变化。完整 `make check` 已执行并返回 0，日志 `.scratch/forge-orbstack/final-scope-check-20261022.log` 明确包含 storage-bootstrap 模块测试通过，以及两数据库、race、vet、build 和 OpenAPI 一致性检查。真实 JuiceFS Docker 探针仍属显式集成检查，不将默认跳过算作本轮执行。

最终候选范围为 artifact-go 65 个路径（包含新增 `.github/workflows/check.yml` 与 `scripts/check.py` 修改）、forge-tenant-api 26 个路径（包含 `configs/env.example`），前端仅以上 16 文件补丁。候选清单不含 `.scratch`、`.local`、bin 或私有输入。运行环境仍为上一节 revision 9；本轮没有部署最终补丁或升级集群，不能把原完整工作区镜像称为由部署专用补丁构建。

本轮 1 个子代理完成补丁修正与隔离复验；真实暂存区保留原来的三个 storage 文件删除，其余未暂存。未提交、推送或创建 Git tag。

## 2026-10-23 三仓合并版本升级与四范围验收（revision 10）

三个仓库的部署相关 PR 合入后，从合并提交重新构建镜像并升级本机 OrbStack 的 `forge` release，完成真实登录、四种 scope 的读写隔离、重启持久性、引用释放和上传授权到期验收。

### 提交与镜像

| 仓库 | 合并提交 | 镜像 |
| --- | --- | --- |
| artifact-go（PR #6） | `6c8d51351cd0146fb6314fe50d7c34109f3e3b50` | `artifact-go:merged-6c8d513` |
| forge-tenant-api（PR #5） | `d392dbdcbf3805b236980d59e4410dd3099ca4d8` | `forge-tenant-api:merged-d392dbd` |
| forge-tenant-web（PR #3） | `5cbbfbaacddb560ea57b8fe18925f64467a28bea` | `forge-tenant-web:merged-5cbbfba` |

三个镜像均从 detached 干净 worktree 的合并提交构建。前端原工作区有未提交功能改动，本轮没有修改、暂存或 stash 该工作区。

### 升级结果

- 升级前执行 `helm upgrade --dry-run=server --hide-secret`；随后 `helm upgrade forge ... --reset-values -f values-orbstack.yaml --wait --wait-for-jobs --timeout 10m` 成功，release `forge` 位于 `orbstack/forge`，revision 10，更新于 2026-10-23T03:14:30Z。
- 外部初始化 Job `forge-external-bootstrap-10` 完成耗时 6 秒；租户 API/worker、Artifact、Web 均 Ready，运行镜像与上表一致。
- Artifact 配置中 `DB.AutoMigrate` 与 `StorageDB.AutoMigrate` 均为 `true`，initContainer 仅有 `storage-iam`，不含独立 migrate 步骤。
- 三块 PVC 保留且 UID 不变：`data-forge-tenant-postgres-0` `4e74f95d-00cb-42cf-8bc2-b3aafdc61a43`、`forge-artifact-data` `ac8b324a-d058-4eae-aa55-15b9fe5baa4d`、`forge-juicefs` `a56e541f-41cb-4a87-b473-da6cbd0528f5`。

### 验收结果

真实 Casdoor 登录在专用浏览器空间中完成，`/api/v1/me` 返回 200。随后对 `tenant-shared`、`tenant-personal`、`project-shared`、`project-personal` 四种 scope 逐个执行文件登记、STS PUT、Complete、发布版本、Resolve 和 STS GET，下载字节与上传一致；创建完成后先重启一次 Artifact 再做核对，四种 scope 的数据跨重启仍可读回。

同时通过：

- 共享引用版本删除后重启 Artifact，引用释放任务跨重启完成；末引用版本仍可读且能取回原始字节，删除末引用后文件最终 `deleted`。
- 上传授权到期保护：授权签发后到期前文件保持 `deleting`，到期后变为 `deleted`。本轮观察时间为 `04:35:37Z`（到期前）与 `04:51:18Z`（到期后），授权到期时间 `04:51:07Z`。
- 跨 scope 读取、Complete、签发授权和删除分别返回 404、404 + `FileNotFound`、404、404；Artifact 版本、draft、alias、retention 和 Resolve 在错误 scope 下返回 404 或 405。
- 错误 CSRF 返回 403，非授权 Origin 返回 403，匿名请求返回 401。
- `requestKey` 重放与参数冲突并发矩阵仍由 `TestCollectionCreateIdempotency` 覆盖，本轮不重复执行。

公开对象 ID 供复查：tenant-shared `b4abbae9-0460-4cfe-80eb-096853ec7296`，tenant-personal `75817bd4-256d-4269-b101-2ac7498b8b93`，project-shared `878187cc-cc67-4dbc-9497-3742057555c4`，project-personal `0b0ed490-f985-459b-ab17-5a58e75e6f7d`，引用释放样例 `f06bc05b-7d0d-43c9-99b3-29f4285daa50`。

验收报告与详细记录保存在被忽略的 `.scratch/forge-merged-20261023/`：`acceptance-report-3.json` 为通过报告（`verifiedAt` 2026-10-23T04:52:18Z），`deployment.json` 汇总 release、镜像、PVC 与验收结论。该目录不进入 Git。

### 执行说明

- 验收由 Ego Lite 浏览器驱动，复用 `deploy/acceptance/forge-redeploy.cjs` 与 `reference-release.cjs` 的操作序列（四 scope、STS、重启、授权到期保护），适配脚本位于上述 `.scratch` 目录；仓库内 `forge-redeploy.cjs` 仍是文档化的默认入口。
- Ego Lite 的 Node 进程创建的可执行文件会带 `com.apple.quarantine`，启动即被 macOS `SIGKILL`。本轮改为在普通 shell 预构建 helper 二进制后供脚本调用；该问题不是服务端故障。
- 前两次尝试都在第一个 STS PUT 失败，未产生通过报告：`acceptance-report.json`（03:28Z）与 `acceptance-report-2.json`（04:26Z）均为 `phase=creating`，另有首份报告的归档副本。第二次加入脱敏诊断后确认退出信号为 `SIGKILL`，即上述 quarantine 问题；第一次使用相同的进程内构建路径，但当时没有诊断输出，按同一原因推断，未单独证实。

### 验证边界

- 首次 rollout 的 Artifact Pod 从 `03:14:40Z` 到 Ready `03:14:47Z`，约 7 秒；最后一次重启从 `04:51:53Z` 到 `04:52:05Z`，约 12 秒。
- 集群 Metrics API 不可用，`kubectl top pods` 失败，因此本轮没有 CPU、内存和容量数据，只有启动耗时观测。
- 验收针对单节点 OrbStack 本地镜像，未推送到镜像仓库，也不代表多节点调度、生产容量或 RPO/RTO。本记录不含凭据、session、CSRF 或 STS 明文。

## 边界

- 外部 Casdoor/GitLab 的集成对象在 Helm 卸载后仍存在；卸载不撤销它们。
- PVC、seed、TLS 与外部输出 Secret 带 keep 策略；删除 namespace 会连同它们一起删除，属于明确重置。
- `helm template`/client dry-run 不含集群 `lookup` 结果，不能作为已有环境的升级清单；升级预览使用 server dry-run。
- 本记录不包含任何凭据、session、CSRF 或 STS 明文。
