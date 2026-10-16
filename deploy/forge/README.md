# Forge 本机集成部署

此目录部署到 `orbstack`：`artifact-storage` 中的独立 PostgreSQL（artifact/storage 两库、两角色）、combined Forge 服务和 TLS sidecar；`forge-tenant-split` 中的 tenant API TLS bridge。使用已有 JuiceFS，保留其数据与管理凭据。镜像 `artifact-go:forge-verification` 必须预先构建。

`orbstack.json` 是无密钥资源清单。`prepare.py` 读取现有 `forge-tenant-env` 的服务令牌和 `storage-credentials`，生成专用管理/交接令牌、私有 CA 和服务证书。所有运行配置与凭据只保存在被 Git 忽略的 `.scratch/forge-orbstack/`，目录 0700，文件 0600。该目录需作为本机部署密钥材料保管；脚本重复执行保留已有 CA 与令牌。

```sh
python3 deploy/forge/prepare.py
kubectl --context=orbstack apply -f .scratch/forge-orbstack/base-resources.json
# 仅全新库首次初始化开启迁移。
python3 deploy/forge/service.py --tenant TENANT_ID --project PROJECT_ID --migrate
kubectl --context=orbstack apply -f .scratch/forge-orbstack/service-resources.json
kubectl --context=orbstack -n artifact-storage rollout status deployment/artifact-forge
# 首次迁移完成后关闭，配置校验自动触发重新部署。
python3 deploy/forge/service.py --tenant TENANT_ID --project PROJECT_ID
kubectl --context=orbstack apply -f .scratch/forge-orbstack/service-resources.json
```

Scope 精确允许指定租户及可选项目；租户本身不通配其项目。更换验收 scope 时重新运行 service.py，覆盖 allowlist，不累积旧范围。内部引用入口只监听同 Pod 的 loopback；公网 Service 仅暴露 TLS 8443，代理业务 HTTP，不暴露引用内部端口。

- Tenant endpoint：`https://forge-tenant-tls.forge-tenant-split.svc.cluster.local`。
- Artifact/storage endpoint：`https://artifact-forge.artifact-storage.svc.cluster.local`。
- tenant/web 需挂载本目录私有运行目录里的 `ca.crt`，保留原有信任根并追加该 CA，不能关闭验证。
- tenant 使用独立 `bucket-management-token` 配置 Storage worker；只为本轮新建租户触发 bucket 开通，不自动 backfill 既有租户。
- 本轮部署用于真实 session、租户 bucket 开通和 JSON 版本发布读取。未配置 STS issuer，grants 路由不启用。
- 此部署未配置生产备份、容量与恢复目标；与四层隔离恢复验收分别记录。

## 真实登录验收

使用兄弟 `forge-tenant-web` 的 Playwright 和 tenant `.local/deployment.json` 私有登录配置。脚本启动独立无头浏览器，不使用用户已有浏览器会话。`prepare` 会新建验收租户和项目；`verify` 发布 JSON 版本、登记空文件元数据，并在缺少负例时新建由另一已激活账号管理的验收租户。这些验收数据会保留，文件登记按存储 TTL 回收。

```sh
FORGE_ACCEPTANCE_DIR="$PWD/.scratch/forge-orbstack" node deploy/acceptance/forge-login.cjs prepare
# 使用输出 tenant/project 更新 service.py 的精确 allowlist 并等待部署完成。
FORGE_ACCEPTANCE_DIR="$PWD/.scratch/forge-orbstack" node deploy/acceptance/forge-login.cjs verify
```

目录必须已存在且权限为 0700。可通过 `FORGE_CONFIG`、`FORGE_WEB_ROOT`、`CHROME_EXECUTABLE` 指定配置、前端路径及浏览器。报告不包含会话或凭据。实际验收见 [部署与登录记录](../../docs/forge-orbstack-login-20261016.md)。
