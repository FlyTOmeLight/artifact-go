# OrbStack 配置收敛与旧实例退役

后续单服务改造已移除内部引用 HTTP/token；当前架构与升级结果见[进程内文件引用](inprocess-storage-20261016.md)。以下配置收敛记录保留当时的部署证据。

2026-10-16，本机 OrbStack 的实际文件链路配置已分别保存到三个服务各自仓库的 Helm chart。完成一次手工字段接管、两次普通重部署，并在旧实例退役后重验同一文件内容。

## 唯一生效的接线

- 前端继续使用 `forge-tenant-web:0.14.1-enterprise-assets`，保留 Agent/Coding/Tenant 路由；artifact/storage metadata 上游均为 `http://artifact`。
- `forge-tenant-split/artifact` 运行 `artifact-go:forge-orbstack-20261016`，保留独立 artifact/storage 数据库、当前 STS issuer、16 分钟授权期限、ConsumerID 和精确 tenant/project scope。
- artifact 到 tenant 使用 `forge-tenant:8443`；tenant worker 到 storage 使用 `artifact:8443`。TLS 由对应 sidecar 和既有私有 CA 提供。
- tenant chart 管理内部 TLS sidecar、8443 Service、worker 专用 Secret/CA；main/migrate/bootstrap 使用同一已验收镜像。共享 Secret 中指向旧服务的 STORAGE_URL/STORAGE_MANAGEMENT_TOKEN 已移除，worker 使用现有 `tenant-storage-integration`。
- 应用非敏感配置在 `charts/artifact/values-orbstack.yaml` 的 `config` 字段，凭据由 `artifact-runtime-credentials` 注入。导入后逐字段对比等于原 TOML 运行配置，未更换数据库、S3 或服务身份。

[部署入口与恢复说明](../deploy/forge/README.md)。三个仓库分别保存自己的部署源文件并独立升级；前端已有但未提交的 Agent/coding-releases chart 路由也是其本机模板的必要部分，其他 UI 改动保持原样。

## 早期脚本重部署验证（已被独立 Helm 部署替代）

初次普通 Helm 4 升级发现字段由此前 `kubectl-client-side-apply` 持有，未跳过冲突。备份后使用一次 `--adopt-live`（Helm `--force-conflicts`）将核对过的字段交给 Helm；后续两次普通 `deploy.sh` 均不带该参数，全部成功。

- 当时 Helm revision：tenant **38**，web **33**。
- artifact、PostgreSQL、tenant API/worker、web 均 Ready。
- 七组现有 Secret 内容指纹在重部署后保持一致；两个业务库 AutoMigrate=false。
- 本轮升级前 tenant/artifact/storage dump、原 Secret 均保存在私有 `redeploy-backup/`。
- Python 语法、Shell 语法、Helm lint/render及缺少 TLS Secret 的负例、公开配置凭据扫描均通过。验收辅助通过 Node/Go 格式、语法和 vet。

## 真实文件验收

通过独立无头 Chrome 正常 Casdoor 登录，未导出会话或 STS 凭据。第一次在旧三组实例已经缩容为零时执行：文件登记、STS PUT、Complete ready、带文件版本发布、STS GET 字节一致。

旧六个 Deployment/Service 删除后再次完整重部署；**23:07:43（Asia/Shanghai）** 从新登录会话读取相同版本及文件，引用与字节都保持不变。

- Artifact：`b41d95fe-e361-48c3-b960-165ab6a02c0b`
- Version：`5c4bb223-8bc0-4f38-8284-fa67cf360fe7`
- File：`bfc602e4-12d7-4a9d-9b05-f0a85f9e7538`

同时确认：旧文件 `93db21eb-f525-4750-bf86-279539144072` 仍为 deleted，新下载授权返回409；错误CSRF返回403，匿名返回401。两次会话均正常登出。正例版本和文件保留，用于日后 `--verify` 检查，不在本轮强制回收。

复现（先按部署说明建立8483/19010转发）：

```sh
# 首次：报告路径不能已经存在，会创建一个保留的带文件版本。
FORGE_REDEPLOY_REPORT="$PWD/.scratch/forge-orbstack/redeploy-acceptance.json" \
  node deploy/acceptance/forge-redeploy.cjs
# 重部署后：只重验已有正例，不再创建文件。
FORGE_REDEPLOY_REPORT="$PWD/.scratch/forge-orbstack/redeploy-acceptance.json" \
  node deploy/acceptance/forge-redeploy.cjs --verify
```

## 旧实例退役与保留

已删除且逐项复查不存在：

- `artifact-storage`：Deployment/Service `artifact-forge`、`artifact-forge-postgres`。
- `forge-tenant-split`：Deployment/Service `forge-tenant-tls`。

旧 preparing/uploading 文件先通过正常 DELETE API 回收，worker 达到 deleted/done；没有改期限或直接删表。旧库4个JSON验收版本保存在原PVC及双库备份，备份已在独立 PostgreSQL17 恢复并逐表核对行数。旧PVC、Secrets和可直接恢复的六资源清单保留在私有 `retired-backup/`；共享 JuiceFS、`storage-data` 和其凭据未删除。细节见[审计](old-deployment-audit-20261016.md)。

本轮仅收敛本机配置，没有扩大文件准入范围，也没有设定生产容量、备份目标或RPO/RTO。

## Helm 所有权修正与复验

2026-10-16 23:36（Asia/Shanghai），部署源改为三个仓库各自的 chart 与 `values-orbstack.yaml`：

- artifact-go 新增 `charts/artifact`，管理两个 Deployment、两个 Service、两个 ConfigMap。现有 PVC 通过 `postgres.existingClaim` 引用，不纳入 release；所有 Secret 仍为外部引用。
- tenant 的 TLS/worker 实现与 OrbStack values 均在 forge-tenant-api；web 的代理配置与 OrbStack values 均在 forge-tenant-web。
- 删除 artifact 仓库跨仓 `deploy.sh`、tenant/web values、重复 `orbstack.json` 及单独 config/nginx 文件；公开应用配置已移入 artifact values。三个服务独立升级，当前无联合发布要求，未添加 umbrella chart。

迁移前保存 `private/pre-helm-resources.json`，确认新 values 与原 ConfigMap 配置语义一致。首次接管因旧 ConfigMap 字段归 kubectl 管理发生冲突，随后使用 `--take-ownership --force-conflicts` 完成接管。再运行无接管参数的正常升级成功：artifact **3**、tenant **39**、web **34**。

六个 artifact 资源均核实归 Helm release `artifact` 管理。原 PostgreSQL PVC UID 保持 `5e9d9735-8408-411f-bcce-d4afccbaa6a9` 且 Bound；七组 Secret 内容指纹全部不变。配置 checksum 负责自动滚动更新，日常不再由 shell 强制重启。

23:36:53 重新真实登录后，原 artifact/version/file 引用及 STS GET 字节校验通过；旧删除文件授权拒绝、CSRF 和匿名拒绝检查通过，会话正常登出。本次 `--verify` 没有新增上传，报告中的 STS PUT/Complete/publish 是前次保留正例的创建证据。临时 S3 端口转发已关闭。

两个子代理分别完成 tenant/web 配置归位。三个 chart 的 lint/template、Node 语法、diff 空白检查通过，web 构建通过。web 的 27 项既有未提交文件经逐字节检查未变。本轮变更尚未提交或推送。
