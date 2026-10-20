# Forge OrbStack 接入与验收

Artifact 的部署源在 [charts/artifact](../../charts/artifact/README.md)，实际公开配置在 `charts/artifact/values-orbstack.yaml`。日常部署直接使用 Helm。

## 部署归属

三个服务各自维护 chart、环境 values 和独立 Helm release：

- artifact-go：`charts/artifact`，release `artifact`。
- forge-tenant-api：`charts/forge-tenant`，release `forge-tenant`；TLS 和 Storage worker 配置由该仓库维护。
- forge-tenant-web：`charts/forge-tenant-web`，release `forge-tenant-web`；同源代理路由由该仓库维护。

各仓库的 `values-orbstack.yaml` 和 chart README 提供各自的部署命令。Artifact 部署无需其他仓库的目录或源码。目前不需要联合发布，因此不引入 umbrella chart，也不在本仓脚本中升级其他服务。以后若需联合发布，应由上层 chart 声明版本化依赖。

当前 namespace 为 `forge-tenant-split`。共享 JuiceFS 位于 `artifact-storage`；保留其 Deployment `storage`、Service `juicefs` 和 PVC `storage-data`。

## 已有凭据

现有 `artifact-runtime-credentials`、`artifact-integration-tls`、`artifact-integration-db`、`artifact-integration-db-init` 继续使用。私有备份位于 Git 忽略的 `.scratch/forge-orbstack/private/`。内部引用交接 token 已不再需要；数据库、S3 和 tenant 接入身份继续使用。

## 本机验收

```sh
kubectl --context=orbstack -n forge-tenant-split port-forward svc/forge-tenant-web 8483:80
kubectl --context=orbstack -n artifact-storage port-forward svc/juicefs 19010:9000
```

两个转发分别保持运行。Forge 登录 Origin 为 `http://127.0.0.1:8483`；metadata 走 `/artifact` 和 `/storage`，客户端 S3 直传走 `http://127.0.0.1:19010`。用户身份与 tenant/project scope 仍由 tenant API 核实；进程内文件引用按请求 scope 查询。验收对象来自 `deploy/acceptance/forge-scope.json`，它只是测试输入。artifact 与 storage SDK 使用 server-go 定义的 `x-forge-tenant-id` / `x-forge-project-id` 选择范围；tenant API 必须部署包含 `/internal/v1/scope/resolve` 且响应带 `principal` 的版本。STS issuer 的 bucket 基础权限不变。

验收工具允许通过 `FORGE_WEB_ROOT` 和 `FORGE_CONFIG` 指定浏览器依赖与私有登录配置路径；它们只用于测试，不是部署依赖。保留已有版本的验证命令：

```sh
FORGE_REDEPLOY_REPORT="$PWD/.scratch/forge-orbstack/redeploy-acceptance.json" \
  node deploy/acceptance/forge-redeploy.cjs --verify
```

旧 `forge-login.cjs prepare` 创建新租户，不适用于当前固定文件 scope。验收与退役结果见 [验收记录](../../docs/deployment-consolidation-20261016.md)。旧六个 Deployment/Service 已退役；原 PVC、Secrets、双库 dump 和恢复清单保留在私有 `retired-backup/`，详见[审计记录](../../docs/old-deployment-audit-20261016.md)。
