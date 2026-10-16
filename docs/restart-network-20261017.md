# OrbStack 重启后请求超时排查

## 原因与修复

重启时，Service 端点状态与实际转发切换有时间差。原 chart 未设置退出等待，旧实例停止监听后仍有连接发往旧端点，导致 web 到 artifact 的 TCP 建连超时。失败发生在应用收到请求之前，与 storage 引用的进程内调用、下载授权逻辑和 tenant 身份校验无关。

chart 对 API 和 nginx 两个容器设置 `preStop.sleep.seconds=10`，保留接收残余流量的窗口；Pod `terminationGracePeriodSeconds=45`，给随后的正常退出留出时间。配置属于 artifact 的 Helm chart，不需要修改 tenant-api 或 web 代码。两个业务库、表结构及数据未改。

Kubernetes 的端点移除与容器退出并行推进，见[官方终止行为说明](https://kubernetes.io/docs/tutorials/services/pods-and-endpoint-termination-flow/)。10 秒是本机验证的窗口，不表示所有环境都具备同样传播时延。

## 可复现证据

原始下载授权失败未保留可安全使用的详细错误，不能重建当时逐包时序。本次使用同一部署、保留版本和真实登录路径，连续五轮重启复现了同类首次请求超时；此次失败可提前出现在版本 GET，证明并非 grant 特有。

- 浏览器请求立即发出，同期 tenant 页面轮询仍在几毫秒内返回；独立 HTTP 请求也曾在同一窗口超时。
- 00:09:30 至 00:09:56（Asia/Shanghai），web `192.168.194.69:59538` 到 artifact Service `192.168.194.166:80` 持续处于 `SYN_SENT`，至少 25.5 秒。新 Pod `192.168.194.81:18080` 没有对应入站连接，artifact 到 tenant `192.168.194.215:8443` 连接正常。
- 加入退出等待后，00:11:40.762 旧 Pod `.82` 已显示 `ready=false、serving=true、terminating=true`；00:11:41.423 仍在退出期间，却新增来自 web 的 TCP 连接。退出前继续监听确实承接了尚未切换完的流量。
- 未读取宿主机 conntrack，不将具体 DNAT 或 OrbStack 内部实现当作已确认事实。

## 修复验证

先保持旧镜像 `forge-inprocess-20261016` 不变，仅更新 chart，排除代码移动的干扰：

- 原复现命令连续两轮通过。重启后版本 GET 为 14/10 ms，下载授权为 28/14 ms，均无重试。
- 40 秒连续匿名请求跨越两次实例切换，共 186 次，全部得到预期 401，无连接错误或超时。
- `forge-redeploy.cjs` 保留 `--restart` 回归入口和脱敏错误分类；临时 CDP、并发对照和 TCP 采样已移除。

最终部署 `artifact-go:forge-storage-app-20261017`，Helm revision 9 / chart 0.2.1：

- 新文件完成 STS PUT、Complete、版本发布及 STS GET 字节一致性校验；版本 `cb5b9d72-6f60-4ef6-947e-a3bea1ca5233`，文件 `4751f335-7e6d-4895-8c30-98eb93bdda56`。
- 新镜像显式重启后立即回读通过，版本 GET 9 ms、下载授权 11 ms，无重试。
- 两个已有版本 `5c4bb223-8bc0-4f38-8284-fa67cf360fe7`、`6754d8ab-7109-4747-b1a2-efaaa76cde19` 及其原文件继续可读、字节一致。
- 三组真实登录验收均确认已删除文件授权返回 409、错误 CSRF 返回 403、匿名返回 401，测试会话正常登出。
- PVC UID 仍为 `5e9d9735-8408-411f-bcce-d4afccbaa6a9`，状态 Bound；两库和持久引用归属未变。
- 目录收敛后的 `scripts/check.py` 全量通过：PostgreSQL 17 / MySQL 8.4、race、备份恢复、vet、三个入口构建。OpenAPI 重新生成后内容不变，独立 Go 审查未发现阻塞问题。

本轮两个子代理分别完成目录与测试/独立审查、重启网络取证，均已完成。记录中的测试只证明本机已覆盖的重启和文件路径，不扩大为生产可用性承诺。
