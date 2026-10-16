# 文件引用收敛为进程内调用

Artifact 与文件管理在一个进程运行。版本/草稿发布及后台回收通过 `internal/app/storage.ReferenceService` 调用 `storage.BindingService`，不再经过内部 HTTP。

2026-10-17 目录收敛：文件管理业务归 `internal/app/storage`，S3 客户端归 `internal/client/storage`，storage 库模型归 `internal/table/storage`。独立 `fileref` 包已删除；引用契约与确认元数据直接归 storage app。

## 变更范围

- 删除 `internal/storagehandoff` 客户端、handler、协议包装和独立监听生命周期。
- 删除 `StorageHandoff`、`StorageInternal`、`Assembly` 及引用交接 token 配置；Forge 入口统一装配两库和文件模块。
- 保留 `StorageConsumerID=artifact-orbstack`，它是数据库内持久引用的归属标识，不是凭据。
- 两库连接、事务、表结构及已有数据保持原样。持久引用计划的 JSON 字段不变，原有释放任务可以继续执行。
- 公开接口的用户身份和精确 tenant/project 查询、tenant bucket 管理鉴权、S3 管理与 STS issuer 凭据保持各自职责。
- Helm chart 继续只部署 artifact，自身公开 HTTP 和对外 TLS 接入保留。删除的是进程内引用 HTTP，不影响 tenant worker 访问 bucket API。

## 验证

2026-10-16 本机验证：

- `scripts/check.py` 通过：独占 PostgreSQL17/MySQL8.4，完整 race 测试、双库备份恢复、发布/草稿/回收、进程退出、vet、三个程序构建。最终 integration 用时142秒。新增四种引用操作的跨 tenant/project 拒绝用例。
- `gorgeous -d ./...`、`git diff --check`、Helm lint/template、Node语法检查通过。重新生成 OpenAPI 后内容不变。
- `deploy/acceptance/four_layer.py` 通过：单 Service/同 HTTP host，两库、JuiceFS 元数据及对象同一冻结点备份恢复；原版本和SHA256字节一致，恢复后待处理释放/物理删除完成，共享引用保留。
- 独立审查发现并修复测试注入暴露 internal 类型的问题；生产 `service.New` 固定创建本地 BindingService，故障替身仅在测试装配中。

## OrbStack 实测（2026-10-16 历史记录）

镜像为 `artifact-go:forge-inprocess-20261016`，由本仓 Dockerfile 构建。默认Go代理出现下载EOF，使用本机已配置的 `--build-arg GOPROXY=https://goproxy.cn,direct` 构建成功。构建上下文通过 `.dockerignore` 排除私有备份。

升级前保存两库dump、Deployment和运行Secret到 `.scratch/forge-orbstack/private/pre-inprocess-*`，备份不进入Git。首次更新镜像遇到历史kubectl字段管理冲突，使用一次 `--force-conflicts` 接管镜像字段；随后普通 Helm 升级不带接管参数成功。

运行 Secret `artifact-runtime-credentials` 仅删除 `STORAGE_CONSUMER_TOKEN`；其余全部字段逐项核对不变。运行ConfigMap没有StorageHandoff/StorageInternal/Assembly，容器网络命名空间已确认无18081监听。原PVC UID保持 `5e9d9735-8408-411f-bcce-d4afccbaa6a9`、状态Bound。

真实Casdoor登录验收：

- 升级前保留的版本 `5c4bb223-8bc0-4f38-8284-fa67cf360fe7` 和文件 `bfc602e4-12d7-4a9d-9b05-f0a85f9e7538` 继续可读、字节一致。
- 新上传文件 `8fb7753b-a5f2-492b-8d1c-da6a1bb8d82a` 完成STS PUT、Complete、版本发布 `6754d8ab-7109-4747-b1a2-efaaa76cde19` 和STS GET字节校验。
- 已删除文件不能获得新授权，错误CSRF返回403，匿名返回401；测试会话正常登出。

测试用scope位于 `deploy/acceptance/forge-scope.json`，不再充当内部服务allowlist。用户scope由tenant API验证，STS issuer的bucket基础策略未改。两个验收版本保留用于后续回归；完整16分钟凭据过期窗口本轮未重跑。

旧HTTP版本的回滚依赖原交接token；需要从私有升级前备份恢复该字段后再恢复旧release配置。新版本日常部署不再使用它。


最终 release 为 `artifact` revision **6**、chart **0.2.0**。普通升级后显式重启进程，23:55:23（Asia/Shanghai）从新登录会话回读新版本和文件通过。紧接重启的一轮在下载授权请求阶段未完成，未保存原始异常以避免会话泄露；新会话复测通过，未确认该次失败的具体原因，本记录不宣称重启期间零中断。

当时使用三个子代理，分别完成生产实现、测试迁移、文档和独立审查，均已完成；临时验收容器/网络/卷及S3端口转发已清理。

## 2026-10-17 后续修正

已删除独立 `fileref` 包，storage 归入 app/client/table 对应目录，重跑双库 race、备份恢复、vet/build 全部通过。已部署 `forge-storage-app-20261017`，新上传发布和两个历史版本回读均通过。重启故障复现为 Service 切换期间的 TCP 建连超时，Helm 增加退出等待后原复现连续通过；完整证据见[重启排查记录](restart-network-20261017.md)。
