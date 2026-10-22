# OrbStack 真实 Forge 登录与 API/Go SDK 联调

日期：2026-10-16。范围：现有 OrbStack，API/Go SDK 首期联调，不增加完整上传 UI。以下保留首次文件联调证据；随后配置已纳入部署文件并完成重复部署验证，见 [配置收敛记录](deployment-consolidation-20261016.md)。

> 历史记录：本文独立应用与共享存储环境已于 2026-10-22 退役，以下地址、配置和结果仅描述当时验收。当前安装与访问见 [Forge 部署说明](../deploy/forge/README.md)。

## 当时的入口与范围

| 用途 | 本机地址 |
| --- | --- |
| Forge 登录 | `http://127.0.0.1:8483` |
| Artifact Go SDK Endpoint | `http://127.0.0.1:8483/artifact` |
| Storage Go SDK Endpoint | `http://127.0.0.1:8483/storage` |
| S3 / STS | `http://127.0.0.1:19010` |

SDK 每次请求携带正常 Forge 登录得到的 Session、CSRFToken、原始 Origin 及 TenantID/ProjectID。不能用服务管理 Bearer 代替用户 session。登录 Origin 保持 `http://127.0.0.1:8483`，与 tenant PublicURL 一致。

本次文件链路准入限定为已登录账号的租户 `t_3cde51fd5938e7b3669d7c44` 及项目 `p_654e7738115adb5dedec772c`：StorageInternal 精确配置租户直属和该项目两个 scope，普通 IAM issuer 的基础策略仅覆盖该租户 bucket 的 GetObject、PutObject、ListMultipartUploadParts、AbortMultipartUpload。每次 STS 再限制具体文件对象和请求动作。当前不是所有租户、所有未来项目均已开通文件交接；扩展时须同步更新精确 scope 和 issuer 基础策略。

既有租户的 bucket 回填任务已执行，查询时 26 项 succeeded。bucket 存在不代表上述文件权限已面向全部租户开放。

旧端口转发操作入口已移除。当时 S3 endpoint 在 artifact Pod 内通过本地 sidecar 转发到 JuiceFS，在宿主机通过 port-forward 使用相同地址；这是已退役环境的本机联调布局。

## 部署变更

- tenant API/worker 使用本地镜像 `forge-tenant-api:artifact-integration-20261016`，保留 AppKey、登录配置及既有数据；增加 scope API 和独立 bucket worker。
- 在现已退役的独立应用环境新增 `artifact`，使用 `artifact-go:forge-orbstack-20261016` 的 Forge combined 入口。artifact/storage 分别使用独立数据库及数据库用户，引用交接仍是本 Pod 回环 HTTP/JSON。
- 新增 `artifact-postgres` 及 1 GiB 元数据 PVC，两个业务数据库分别为 artifact、storage。原 JuiceFS 10 GiB PVC 不变；首次建表后已关闭两库 AutoMigrate，并重启验证。
- tenant scope 调用和 tenant 到 storage 管理调用通过内部 8443 TLS；使用专用本地 CA，客户端校验证书。管理、交接、tenant 服务调用和 S3 issuer 凭据分离。
- 现有前端 Nginx 仅新增 `/artifact/v1/`、`/storage/v1/` 代理，保留原 Cookie/Origin/CSRF；没有修改前端页面。内部接口不公开。
- STS 默认/上限设为 16 分钟，保留最低签发期限及预算；使用独立普通 IAM 用户，不以 root 签发 STS。

首次联调直接更新了 Kubernetes 资源。后续已把 TLS、worker 和代理配置纳入对应 chart/values，artifact 配置见 `deploy/forge/`，正常重复部署通过；生产环境部署仍需独立配置。

## 实测结果

1. 升级前真实 tenant 数据库已备份；副本恢复后连续执行两次新版 migrate 均成功。仅新增 tenant_bucket_provisions 及其约束/索引，原表没有 DDL 变化，原数据 dump 一致。随后实际迁移 Job 成功。
2. 独立无头 Chrome 使用现有私有测试账号走账号密码、Casdoor 授权码回调和 Forge session。升级后再次从全新浏览器上下文登录，me、租户及项目查询均为 200；没有接管用户浏览器、插入伪造 session 或修改账号/成员。
3. 真实 Go SDK 经上述同源入口通过：匿名 401、错误 CSRF 403、无归属项目 404；JSON 创建/发布；文件登记、STS PUT、Complete、ready 文件 GET；带文件草稿创建/发布、Resolve、下载；跨 artifact 复用；删除非末引用后仍可读；末引用释放后拒绝新读取授权。测试 artifact 已归档。
4. 该账号的既有项目 GitLab write=false，但其 active 租户成员及项目归属符合物料准入，物料发布成功；没有扩大 GitLab 权限。
5. `/artifact/internal/...`、`/storage/internal/...` 和根 `/internal/...` 均为 404；两个命名空间的 metrics 路由为 404。根 `/metrics` 返回前端 SPA HTML，不是 Prometheus 数据。
6. 关闭自动迁移、重启 artifact 后，真实会话查询 me 和 artifact 列表仍为 200；五个相关 Deployment 均 Ready。

测试文件 `93db21eb-f525-4750-bf86-279539144072` 已进入 deleting；最后检查有一项正常 pending 的 storage 删除任务，notBefore 为 **2026-10-16 22:37:18（Asia/Shanghai）**。这是已签发写凭证的保守到期上界，没有修改数据库期限或强制删除。后续只读查询已确认该文件为 `deleted`、delete task 为 `done`，未改写期限；此前隔离 JuiceFS 的真实到期验收仍见 T12 记录。

## 恢复材料与维护

本轮升级前数据库 dump、部署/Service/代理配置、原 Secret、专用 CA 和运行配置保存在 Git 忽略目录 `.scratch/forge-orbstack/private/`，目录 0700、私有文件 0600。它们用于本机恢复与后续接线，不得提交。运行中的 Secret 名称包括 artifact-integration-config、artifact-integration-tls、tenant-storage-integration；本文不记录密钥值。

回退入口/镜像先恢复保存的配置；本次新增任务表可保留，不应直接恢复旧数据库覆盖升级后的业务写入。需要数据库恢复时，先停止相关写入，核对备份时间与之后的数据。不要删除现有 JuiceFS PVC 或引用墓碑。

首期剩余边界：本机端口转发与静态 scope 扩展、正式环境容量/备份/密钥管理和生产发布；完整上传 UI 不在本轮范围。本轮没有缺少需要用户补充的账号或配置。
