# OrbStack 部署与真实登录验收，2026-10-16

本轮已将 Forge 身份接入部署到本机 OrbStack，并通过独立无头 Chrome 完成真实 Casdoor 登录和同源浏览器请求。生产参数仍按约定后定。

> 历史记录：本文独立应用与共享存储环境已于 2026-10-22 退役；版本、资源快照与实测结果保持为当时证据。当前安装与访问见 [Forge 部署说明](../deploy/forge/README.md)。

## 提交范围

- artifact-go `70241bb`：Forge session SDK、身份适配、可选入口及四层联合恢复。
- forge-tenant-api `53b468b`：用户明确批准先提交的既有框架/领域重构基线；`4012155`：本轮 scope resolve 和持久 bucket 开通。此前未接受的历史迁移清理仍按项目背景跟进，本轮没有把它当成已完成的干净 MVP。
- forge-tenant-web `e3a9535`：同源代理；`d201d15`：可选 CA bundle 挂载。仅暂存本轮改动块，原有 27 条未提交改动保留。

## 实际部署

| 组件 | 结果 |
| --- | --- |
| tenant API/worker | 已退役的独立应用环境，Helm revision 34；镜像 `forge-tenant-api:artifact-integration-20261016`，均 Ready |
| 前端 | Helm revision 30；保留原 `forge-tenant-web:0.14.1-enterprise-assets` 镜像与既有 Agent/Coding/Tenant 路由，仅更新代理及 CA 配置 |
| artifact/storage | `artifact-storage` 中 combined Forge 服务，镜像 `artifact-go:forge-verification`；独立 PostgreSQL 的 artifact/storage 两库、两角色 |
| 内部网络 | 私有 CA 校验的 tenant TLS bridge 和 artifact TLS sidecar；引用交接端口仅监听 Pod loopback |
| 数据库迁移 | tenant 原库先 pg_dump，再恢复到独占副本，当前迁移连续执行两次通过；原 13 用户、24 租户保留。artifact/storage 初次初始化后关闭 AutoMigrate |

运行配置、证书和专用凭据保存在忽略的私有目录，未进入 Git。tenant 原数据库及原 Secret 回退材料保留在其 `.local/artifact-deploy-20261016-221436/`。旧部署操作入口已移除，当前部署见 [deploy/forge](../deploy/forge/README.md)。

## 浏览器和任务证据

验收脚本 [forge-login.cjs](../deploy/acceptance/forge-login.cjs) 使用真实登录页面，观察到 Casdoor `/api/login` 200、`/auth/callback` 303；随后在页面内通过 `fetch` 发起同源请求，沿用浏览器 Cookie、原始 Origin 和 CSRF，不向数据库插入会话。

专用验收租户为 `t_20da31771c89365a2b93e6dd`，项目为 `p_c45938e9a260cab87f76107a`。引用交接 allowlist 精确包含这两个范围，未通配其他项目。

- 浏览器创建租户及项目成功。`tenant_bucket_provisions` 首次尝试即 `succeeded`，backend 为 `juicefs-local`，bucket 为 `tenant-t-20da31771c89365a2b93e6dd`，无 LastError；未执行既有租户全量 backfill。
- 租户与项目分别创建物料、发布 JSON 版本、以相同 requestKey 重放、读取内容，均成功；createdBy 和 scope 与真实登录身份一致。
- 两个 scope 相互读取同一版本均返回 404；错误 CSRF 返回 403；无会话返回 401。
- 另建指定其他管理员的验收租户，平台管理员本人没有成员资格，访问 artifact 返回 403。
- 同源 storage 文件登记成功，返回 uploading，证明自动 bucket 开通后的 metadata 调用可用。

本次成功样本：租户物料 `244f3371-3ccc-45ab-8ccd-4b9544eb22b7`，版本 `bab0a050-6685-49f4-abfc-c17b0f339d69`；项目物料 `501e30d6-56b2-49b3-90d8-1f44711b25cc`，版本 `0d21640b-9709-4922-a5b6-ca51624f6a34`。验收租户与这些记录保留供复查。

## 验证边界

本轮验证真实登录与 JSON 版本闭环；当时部署未配置 STS issuer，未启用 grants，未验证真实文件字节上传或文件选择 UI。前端仍是现有工作台，物料请求由登录后的浏览器执行验收脚本触发。生产容量、备份位置、RPO/RTO 未设定。

提交前复用上一轮完整双库/race 检查；tenant 拆分基线另跑真实 PostgreSQL 全仓 race，前端从干净暂存快照执行 build、代理回归及 Helm 检查。部署后验证实际 readiness、TLS 信任、迁移结果和上述浏览器链路。远程 CI 状态单独核实，不以本机验收代替。
