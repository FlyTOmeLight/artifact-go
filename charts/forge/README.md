# Forge Helm 安装

此 umbrella chart 安装 tenant API/worker、Web、Artifact/Storage、两套 PostgreSQL 与 JuiceFS。所有新资源属于同一 release 和 namespace。Coding 与 Agentic 在此安装配置中关闭；不需要它们的 Deployment、数据库或上游地址。

## 安装输入

安装包应已包含固定版本依赖和镜像。构建者从源码执行 `helm dependency build charts/forge`、`helm package charts/forge`；安装者只需 chart 包，不需要 sibling 仓库、Python 或 kubectl 初始化脚本。

公开配置见 `values-orbstack.yaml`。`global.forge.tenantImage` 为 Tenant 服务与初始化 Job 指定同一个镜像，镜像须同时包含 `/forge` 和 `/forge-init`。另需外部 Casdoor/GitLab 的管理员授权及 issuer CA，不能通过生成随机字符串替代：

- `global.forge.external.caPEM`：真实外部证书信任链。
- `bootstrap.authorization.casdoorClientID`、`casdoorClientSecret`：可注册 Forge 应用和用户的外部管理客户端。
- `bootstrap.authorization.gitlabToken`：可签发、查询和撤销本账号 PAT 的 GitLab 管理员 API 授权。
- 或设置 `bootstrap.existingSecret`，引用**本 namespace** 中包含 `CASDOOR_ADMIN_CLIENT_ID`、`CASDOOR_ADMIN_CLIENT_SECRET`、`GITLAB_ADMIN_TOKEN` 的 Secret。

`external.casdoorURL` 与 `external.gitlabURL` 接受集群内 HTTP 或 HTTPS 源地址，但仍拒绝 URL 内嵌凭据、query 和 path；管理员凭据会穿过该链路，生产环境应使用 HTTPS。`publicURL` 仍要求 HTTPS 或 loopback HTTP。

授权通过受限 values 输入或 stdin 提供，不放入公开 values、Git、日志。Helm 将 values 和渲染清单保存在 release 记录中，提供授权的身份应有相应权限。业务服务只得到新注册的专用凭据，不挂载 bootstrap 管理员输入。

### release 与 namespace 名称限制

chart 对所有派生名称都不做截断，而是在渲染入口直接拒绝不符合限制的组合。`values.schema.json` 不控制 `Release.Name` 和 `Release.Namespace`，因此由模板报错：

- release 名称只允许小写字母、数字和连字符，长度最多 36。最长派生资源名是 `<release>-artifact-integration-nginx`（36 字符时为 63），bootstrap Job `<release>-external-bootstrap-<revision>` 随 Helm revision 增长，当前上限留到 7 位 revision。
- release 与 namespace 长度之和最多 51。外部 Casdoor 应用名 `<namespace>-<release>-management` 为两者之和加 12，tenant CLI 要求 bootstrap 身份名为 2–63 字符。
- namespace 至少 2 字符，且以小写字母开头。它会作为 OIDC/管理应用名和 GitLab token 名称的前缀。

报错信息会给出实际长度和超限字段，但只包含 release/namespace 名称，不包含渲染出的 Secret 内容。常规短名称（例如 `forge`/`forge`）不受影响。

```sh
helm install forge ./forge-0.1.0.tgz \
  --kube-context orbstack --namespace forge --create-namespace \
  -f values-orbstack.yaml -f /path/to/restricted-authorization.yaml \
  --wait --wait-for-jobs --timeout 10m
```

该命令创建所有 Kubernetes 初始化资源，完成数据库迁移、内部服务 key 注册、外部 Forge 集成注册及 JuiceFS IAM。外部 Casdoor/GitLab 应已可达；chart 不重新配置共享 GitLab OmniAuth provider，不修改其回调，也不重启 `tenant-lab`。

OrbStack 默认浏览器入口为 `http://127.0.0.1:8483`，S3 客户端入口为 `http://127.0.0.1:19010`。需将 Web Service 的 80 端口与 Artifact Pod 的 19010 端口转发到对应本机端口；这只是访问方式，不参与初始化。也可设置可达的 ingress/LB 地址。S3 Host 参与签名，代理必须保留 Host；Artifact Pod 使用自己的 19010 代理访问同一 JuiceFS。

浏览器登录还要求 OIDC issuer（`global.forge.external.issuer`）从发起登录的浏览器可达。使用 `tenant-lab` 的集群内 issuer 时，本机 Chrome 无法直接访问，需要为 `tenant-lab/identity` 提供 ingress/LoadBalancer 或在能访问集群网络的机器上打开登录页。

## 初始化与持久化

- Helm 生成一个保留的 seed Secret，派生数据库、内部管理和 STS 凭据。升级读取该 seed，不重新生成密码。
- 内部 CA 与叶证书一组保存。升级复用它们；外部 CA 来自输入，不能用新内部 CA 替代。
- Tenant 复用已有 migrate/bootstrap，并注册预生成服务 key。Artifact 的 `DB.AutoMigrate` 与 `StorageDB.AutoMigrate` 均为 `true`，服务启动时通过现有 `service.New` 和 `service.NewStorage` 执行 GORM AutoMigrate，保留当前 schema 的外键约束。Artifact/Storage 不使用独立迁移命令或 migrate initContainer；`storage-iam` initContainer 继续负责 JuiceFS IAM 初始化。
- JuiceFS 使用 Redis AOF 与对象目录所在的持久卷；只对空元数据库初始化，已有 metadata 不重置。
- STS issuer 是普通 IAM 用户，具有明确基础 policy；root 只用于初始化和桶管理，不直接签发客户端 STS。
- `forge-tenant-init` 子 chart 提供外部初始化普通 Job，执行 `/forge-init external-bootstrap`，预算为 300 秒。它读取镜像内 `conf/server/config.toml`，复用服务地址与 CA，`[Bootstrap]` 仅接收证书名、初始账号、邮箱和 PAT 到期日。namespace/release 由 Pod/Helm 注入，目录和资源名由程序约定。
- 初始化 Job 仅能 get/patch 一个指定的输出 Secret；API/worker 无 Kubernetes 写权限。业务 Pod 先执行 `/forge-init wait-external`，通过 Secret volume 等待完整凭据，再依次迁移、登记服务 key 和 bootstrap。普通 Job 与这些 initContainer 共同保证顺序，避免 post-install hook 与 `helm --wait` 互等。
- 升级时已有完整凭据可直接复用；`--wait-for-jobs` 确保 Helm 等待本次初始化 Job 成功。初始化管理员输入只挂到 Job，生成的运行时 GitLab PAT 仍保留现有管理员权限。
- 外部专用应用使用稳定名称；已有有效 PAT 沿用。丢失一次性签发结果时先定位并撤销本轮孤儿 PAT，再重新签发，不无限重试。

数据 PVC 与关联 seed/证书/凭据均保留。`helm uninstall` 不表示清空数据或撤销外部集成；同 release 重装应复用保留的材料。缺 seed 却存在旧卷时安装失败，不能用新密码接管旧数据。删除 namespace 会连同保留资源一起删除，属于明确的重置操作。

不要将离线 `helm template` 输出 apply 到已有环境：离线渲染无法执行集群 `lookup`。升级预览使用 `helm upgrade --dry-run=server --hide-secret`，避免打印 Secret。普通升级使用同一 chart 和公开/私有输入；初始化 Job 重新核对状态，不自动轮换有效凭据。服务 key/PAT 到期及证书续签是显式维护操作。

JuiceFS 管理初始化工具是独立镜像/Go module，复用 `madmin-go/v2`（AGPL-3.0）；它不链接进 Artifact 运行服务。

## 验收

Helm Ready 不能替代业务验收。检查真实登录、本人共享/个人及租户/项目四类 scope、跨范围拒绝、文件注册→STS PUT→Complete→发布→Resolve→STS GET 原字节，再验证重启回读和引用释放后物理删除。部署验收报告应区分静态渲染、隔离测试和真实环境结果。

旧独立环境已退役，相关部署输入和初始化脚本已移除。环境清理是单独的运维操作，不包含在本 chart 的安装、升级或卸载生命周期内。
