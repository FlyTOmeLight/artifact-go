# Forge 接入与恢复交付，2026-10-16

后续已完成提交整理、本机部署及真实 Casdoor 浏览器联调，见[部署与登录记录](forge-orbstack-login-20261016.md)。以下保留实现阶段的原始验收边界。

范围：按用户确认的有效租户成员准入、项目仅校验归属、active-only 接入实施。包含 artifact-go、forge-tenant-api 和 forge-tenant-web；没有提交、推送、发布或升级已有部署。保留各仓库任务开始前的未提交改动。

## 已实现的结果

- tenant 新增受服务 Bearer 保护的 `POST /internal/v1/scope/resolve`。真实 session 校验保留原 Method/Origin/CSRF；要求 active 用户、tenant 与租户成员；项目只查归属，GitLab 未同步或没有项目 grants 仍可进入。平台 operator 无成员时同样拒绝。suspended、已撤销成员、不存在和历史 scope 不会变成 active/deleted。
- tenantv1 增加 ResolveScope，严格校验相同 scope、显式 project_id null/字符串及非空规范 UserID。服务故障不转成归属删除。
- 两个物料 SDK 逐请求传 Session/CSRFToken/Origin，关闭共享 CookieJar 和自动重定向，保留现有 Bearer 管理调用。响应 Set-Cookie 不能污染后续用户或空身份请求。
- artifact 可选 `cmd/forge-server` 支持 artifact-only、storage-only、combined 装配；身份传输由 Runtime 关闭，业务层仍通过现有 resolver/verifier 注入。无有效配置拒绝启动，tenant API、bucket 管理和引用交接凭据不可复用。没有未发布 Go 依赖或永久 replace。
- tenant 创建在同一事务登记独立 `tenant_bucket_provisions`。worker 在事务外开通 bucket，具有独立租约、有限失败预算、退避、回填和 failed 单租户重排，不受 GitLab operation supersede 或确认影响。配置缺省只保留义务，不自动访问 storage。
- 前端 Helm/Vite 增加默认关闭的 artifact/storage 同源代理，仅转发各自 `/v1/`，保留原 Cookie/Origin/CSRF/scope。内部及越界路径拒绝；HTTPS 验证 CA，不扩大 Cookie Domain。没有新增 UI 或修改登录交互。

接口、文件职责与配置见[接入方案](tenant-integration-design-20261016.md)、[部署说明](../deploy/artifact/README.md)，tenant 侧操作见 sibling `forge-tenant-api/docs/storage-bucket-provisioning.md`。

## 实际检查

| 检查 | 结果与边界 |
| --- | --- |
| `ARTIFACT_TEST_TENANT_REPO=<tenant checkout> make check` | 通过；真实 PG17/MySQL8.4、race、vet、三条命令构建、格式及 OpenAPI 生成一致性；integration 129.611 秒 |
| tenant 全仓 `go test -race -count=1 -timeout=5m ./...` | 使用本轮独占 PostgreSQL，通过；service 10.925 秒，授权/成员/配置/SDK 与 bucket 包均执行 |
| 真实跨仓库身份 | tenant 独立 Go 子进程的生产 HTTP/session handler + artifact host/adapter/SDK，PG/MySQL 均覆盖租户及项目、创建/发布/重放/读取、无 session、非成员、CSRF 和跨 scope 拒绝 |
| Forge 入口进程 | 实际构建运行 cmd/forge-server，PG/MySQL 均验证无 session 401、SDK 创建成功、SIGTERM 正常退出 |
| 前端代理 | npm build、改动范围 Prettier、Helm lint/default/enabled/非法上游检查通过；独占 Nginx 与 Vite 实测路径、查询、原始身份头、默认关闭、内部拒绝，Nginx 8MiB 原生限制 |
| Vite 路径回归 | `node tests/metadata-proxy.cjs` 通过；保留原始 `..` 和 `%2e%2e` 的请求不能越过 namespace/v1，拒绝请求不会到达上游 |
| 交付镜像 | artifact-go:forge-verification 本地构建成功，非 root；maintenance help 退出 0，通用及 Forge 入口在无身份配置下退出 1；无网络 smoke，不连接数据库，未推送 |
| 四层联合恢复 | 两业务库+JuiceFS metadata+objects 同一冻结点实际恢复到全新资源，原版本/字节和删除义务均通过；详见[独立记录](four-layer-recovery-20261016.md) |

完整检查使用一次性独占数据库，未连接既有业务数据库。测试中的 session、用户、成员与 scope 来自新隔离 schema，未导出真实业务凭据。跨仓库 fixture 仅向受限 0700 临时目录写本轮测试身份，文件 O_EXCL/0600；默认未设置环境时明确跳过，不把跳过算通过。

## Review 与修正

三个子代理分别负责 tenant scope、artifact adapter/联合验证、bucket/代理配置，并交叉审查。已关闭 SDK CookieJar 串用户、重定向转发凭据、tenant SDK 接受缺失 project_id/空白 UserID，以及 Vite 原始路径和规范化路径不一致导致的越界问题。最终独立复核没有遗留阻断发现。四层恢复补上 worker 启动前待删对象的 SHA-256，避免空位置删除误报成功；恢复阶段完全不挂载源对象卷。

## 部署与验证边界

当前改动在本地工作区；既有 tenant 运行版本、前端和存储部署未升级。接入上线需部署配套 scope API、迁移 bucket 表、配置独立凭据与同源上游，并按部署说明启用 worker/回填。production CA/网络、容量、备份目的地及生产 RPO/RTO 后定。

跨仓库测试证明真实 session 校验与物料 HTTP 业务契约，但 session 在隔离数据库生成，不是人工登录 Casdoor 的浏览器验收。代理检查没有执行完整文件选择/分片/续期 UI。四层演练使用显式可信测试身份和 S3 测试操作者，未验证 STS 授权；既有真实 JuiceFS STS/到期证据仍按 T08/T12 记录判断。历史 deleted scope 本轮按已确认范围拒绝。


镜像构建最初因默认 proxy.golang.org 返回 EOF 失败；核实本机 Go 使用无认证 goproxy.cn 后，为 Dockerfile 增加 GOPROXY 构建参数并使用相同代理完成。依赖版本与 go.sum 未变，校验未关闭。本地镜像 digest 为 `sha256:362a54dedb21a252a2a4a16b976577ed44cc0c88b4eaf41c836b964d6c231d8f`，仅用于 review，未推送。
