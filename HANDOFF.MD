# 项目经验

- 为 Artifact/Storage 初始化表结构时，沿用 `service.New`、`service.NewStorage` 的 GORM AutoMigrate；Forge 部署开启 `DB.AutoMigrate` 与 `StorageDB.AutoMigrate`。不要为 GORM 建表另建 migration CLI 或 migrate initContainer，`storage-iam` 初始化保持独立。
- 保留当前 schema 的外键约束。`internal/table/schema.go` 在 AutoMigrate 后显式创建外键，因为 server-go 默认禁用 AutoMigrate 自动建外键；简化部署入口时不能顺带删除这些约束。
- 以上边界来自本轮用户明确要求；文档已按此更新，实现与验证以当前代码和检查结果为准。历史设计、验收记录只说明当时状态，不作为当前契约。
- 通过 Ego Lite 跑本机验收时，不要在 `ego-browser nodejs` 进程里编译或复制 helper：Ego 创建的文件带 `com.apple.quarantine`，启动即被 macOS `SIGKILL`（表现为 `status: null`、无 stderr、无退出码，容易被误判为服务故障）。在普通 shell 预构建到固定路径，或对产物执行 `xattr -d com.apple.quarantine`。验证依据：同一二进制哈希，普通 shell 构建可直接运行，Ego 复制的副本必被 `SIGKILL`，去掉该属性后恢复正常。
- 跑 Forge/JuiceFS 的 S3 合同探针时，测试 bucket 必须用 `tenant-t-<24 hex>` 命名。chart 的 `storage-iam` init container 只给 issuer 绑定 `arn:aws:s3:::tenant-t-*/*`；用 `artifact-contract-probe-*` 之类的名字会得到 `AccessDenied`，这是策略正确拒绝，不是 issuer 缺权限。验证依据：同一 issuer 对 `tenant-t-000000000000000000000099` 写入成功，对非租户命名 bucket 拒绝。
- JuiceFS 1.4.1 对 bucket versioning/lifecycle API 返回 `NotImplemented`，bucket CORS 返回 `NoSuchCORSConfiguration`；root 不允许 `AssumeRole`。探针把这些记为 pending，改用网关 `OPTIONS` 预检和 storage worker 的过期分片中止路径验证，不能把 API 未实现当成业务失败。
- Forge Web 的 CSP 只有 `default-src 'self'` 时，浏览器会在发出 S3 请求前直接拦截；网关 CORS 预检通过不代表 UI 直传可用。验证依据：同一 Chromium 从干净测试页完成签名 `PUT/GET/HEAD/Range`，从 `forge-web` 页面执行 fetch 时控制台报 CSP violation、`requestfailed` 为空。`forge-tenant-web` 已新增 `csp.connectSrc`，OrbStack overlay 配了 `http://127.0.0.1:19010`；2026-11-09 release `forge` 已升级到 revision 11，运行中 Pod 确认生效，并从同源 `/runtime-config.json` 完成四项签名请求。改 CSP 时同时递增 `web.configRevision`，否则旧 Pod 不会重载 nginx；未登录 `/` 会跳转，浏览器验收使用同源稳定文档。
- `scripts/verify_juicefs_t08_browser.py` 可用 `ARTIFACT_TEST_BROWSER_ORIGIN` 复用既有页面，或用 `ARTIFACT_TEST_BROWSER_PORT` 在指定端口启动干净测试页；验证网关 CORS 时应使用干净页面。验证 Forge Web CSP 时用 `ARTIFACT_TEST_BROWSER_ORIGIN=http://127.0.0.1:8483` 配合 `ARTIFACT_TEST_BROWSER_PAGE_PATH=/runtime-config.json`，避免未登录 SPA 跳转。
- Forge 的对象存储 IAM 是部署级模型：只有一个普通 issuer 用户，基础 policy 覆盖 `tenant-t-*/*`；租户隔离靠 Artifact 每次鉴权后签发精确到 key 和 action 的 STS session policy。不要按租户创建 IAM 用户，也不要把 `AssumeRole` 当成 AWS 跨账户角色；JuiceFS 忽略 `RoleArn`。
- Forge 本机登录验收遇到 `IDENTITY_NOT_ENROLLED` 时，先检查浏览器是否残留旧的 `forge_identity` cookie；旧 cookie 会把 Casdoor 鉴权带到已废弃账号。清理后重新登录。旧 TLS 证书过期时，同时续签 `tenant-lab` 的 CA 与服务端证书，并更新 `forge-tls` 的 CA 链。
- 不要给 storage Adapter 补 `Open`/`io.ReadCloser` 或“阻塞读取退出”测试：设计上 Adapter 只有 EnsureBucket、CreateGrant、Stat、Delete 等元数据/删除能力，字节读取由客户端用 STS 直连 S3。取消测试只需验证 Adapter 操作保留 `context.Canceled`。
- 沙箱内直接跑 `gorgeous -d` 可能与 `make check` 结果不一致：同一个文件在沙箱里显示 `pretty`，提权执行 `make check` 时却报 `ugly`。以 `make check` 为准；需要重写时在提权 shell 执行 `gorgeous <file>`，不要因为沙箱内的 `pretty` 就跳过格式修复。
