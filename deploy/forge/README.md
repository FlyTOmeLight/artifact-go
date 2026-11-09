# Forge 部署与验收

Forge 的全部部署资源由一个 umbrella chart 提供：[charts/forge](../../charts/forge/README.md)。安装、升级、凭据与证书生命周期、初始化顺序都以该 chart 的 README 为准；本文件只说明 OrbStack 上如何访问和验收。

原独立应用与共享存储环境已在 2026-10-22 退役，相关 namespace、独立 release、JuiceFS 和 Secret 已删除，旧部署输入与脚本已移除。当前 Forge 部署为 namespace `forge` 中的 release `forge`。

## 访问

```sh
kubectl --context=orbstack -n forge port-forward svc/forge-web 8483:80
# 引用释放验收会重启 Artifact；转发退出后重新选择就绪 Pod。
while true; do
  kubectl --context=orbstack -n forge port-forward deployment/forge-artifact 19010:19010
  sleep 1
done
```

Forge 登录 Origin 为 `http://127.0.0.1:8483`；metadata 走 `/artifact` 和 `/storage`，客户端 S3 直传走 `http://127.0.0.1:19010`。S3 Host 参与签名，端口转发必须保留 Host。Artifact Pod 内部的 19010 代理指向同一 JuiceFS gateway，不需要额外转发。

浏览器直传还要求 Forge Web 的 CSP 放行 S3 origin。`forge-tenant-web` chart 通过 `csp.connectSrc` 配置 `connect-src`；本仓库的 `values-orbstack.yaml` 已放行 `http://127.0.0.1:19010`，其他环境需替换为客户端实际访问的存储 origin。未配置时浏览器会在发出请求前拦截，网关 CORS 通过不代表浏览器 UI 路径可用。修改 CSP 后同时递增 `web.configRevision`，让 Deployment 滚动重建；只更新 ConfigMap 不会让运行中的 nginx 重载配置。未登录访问 `/` 会在 401 后跳转，可用 `ARTIFACT_TEST_BROWSER_ORIGIN=http://127.0.0.1:8483` 配合 `ARTIFACT_TEST_BROWSER_PAGE_PATH=/runtime-config.json` 选择同源稳定文档做浏览器验收。

浏览器登录还要求 OIDC issuer 从发起登录的浏览器可达。使用 `tenant-lab` 的集群内 issuer 时，本机 Chrome 无法直接访问，需要 ingress/LoadBalancer 或在集群网络内的机器上打开登录页。

## 验收

```sh
FORGE_CONFIG=/path/to/restricted-login.json \
FORGE_TEST_SCOPE=/path/to/scope.json \
FORGE_REDEPLOY_REPORT="$PWD/.scratch/forge-orbstack/forge-redeploy-report.json" \
  node deploy/acceptance/forge-redeploy.cjs

# 重启后复用报告核对同一批对象
FORGE_CONFIG=/path/to/restricted-login.json \
FORGE_TEST_SCOPE=/path/to/scope.json \
FORGE_REDEPLOY_REPORT="$PWD/.scratch/forge-orbstack/forge-redeploy-report.json" \
  node deploy/acceptance/forge-redeploy.cjs --verify --restart
```

`FORGE_CONFIG` 必填，包含 `publicURL`、`adminUsername`、`adminPassword`；也可用 `FORGE_CONFIG_STDIN=1` 从 stdin 读取。scope 必填：写在登录配置的 `scope: {TenantID, ProjectID}`，或用 `FORGE_TEST_SCOPE` 指定 scope JSON。租户 ID 必须是 `t_`，项目 ID 必须是 `p_`。脚本不会读取旧 `.local` 缓存或既有的 Secret。

重启默认目标为 context `orbstack`、namespace `forge`、Deployment `forge-artifact`，可用 `FORGE_KUBE_CONTEXT`、`FORGE_NAMESPACE`、`FORGE_ARTIFACT_DEPLOYMENT` 覆盖。浏览器依赖默认取 `../forge-tenant-web/node_modules/playwright`，可用 `FORGE_WEB_ROOT` 指定其他位置。

脚本覆盖的内容、阶段持久化和等待逻辑见 [验收说明](../acceptance/README.md)；2026-10-21 至 10-23 的真实安装与验收结果见 [验收记录](../../docs/orbstack-rebuild-acceptance-20261020.md)。

直接跑对象存储合同探针时，测试 bucket 必须符合 `tenant-t-<24 hex>` 命名：Artifact 的 `storage-iam` init container 只给 issuer 绑定 `arn:aws:s3:::tenant-t-*/*`。其他名称被 `AccessDenied` 拒绝是预期结果。JuiceFS 1.4.1 不支持 bucket versioning/lifecycle/CORS API，探针会把这些项记为 pending；网关 CORS 用实际 `OPTIONS` 预检确认，浏览器路径用 `scripts/verify_juicefs_t08_browser.py` 的签名 `PUT/GET/HEAD/Range` 验证，未完成分片由 storage worker 在文件过期或删除时按 key 中止。
