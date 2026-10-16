# 本机部署验收，2026-10-15

2026-10-16 已补齐本机四层联合恢复；本页保留先前两层演练的历史结果，最新恢复结果见[后续交付记录](four-layer-recovery-20261016.md)。

状态：本机 OrbStack 隔离验收通过；未部署生产。现有 Kubernetes 服务、PVC、数据库和凭据未改动。生产参数由部署方后续确定。

## 已实际执行

运行 `python3 deploy/acceptance/run.py`，仅使用本机已有 PostgreSQL 17、Redis 7.4.2、JuiceFS 1.4.1 arm64 和 BusyBox 1.37.0 镜像。每轮随机命名独立 bridge 网络、容器和卷，宿主随机端口仅绑定 `127.0.0.1`。未读取现有集群 Secret。

| 项目 | 真实动作和结果 |
| --- | --- |
| 内部 HTTPS | 真实 `storagehandoff.Client` 访问 TLS server，默认信任链拒绝未知 CA；给 transport 加入探针 CA 后成功 Bind/Query/Release。未关闭证书校验 |
| 服务密钥轮换 | 真实 `NewHandler` + `BindingService` + 独立 PostgreSQL，按旧 token → 新旧并存 → 仅新 token 重新装配 handler；ConsumerID=`artifact` 保持不变，原 active 引用仍可查询，撤下旧 token 后精确返回 401，新 token 可释放原引用 |
| 权限范围 | 新 token 仍只允许精确 tenant scope，越界 tenant 返回 403 |
| JuiceFS 小样本 | 8 个不同内容的 1 MiB 对象，并发 4；经 S3 SDK 写入后全部读回并核对 SHA-256。最终复核的写读 31.571 ms，恢复后的读 16.793 ms；包含探针 payload 生成与 hash，不是服务端延迟分位数 |
| 元数据与字节成对备份 | 停止本轮源 gateway，`juicefs dump` 导出 Redis 元数据，复制全部底层对象目录；冻结后的 dump+复制耗时 0.628 s |
| 独立恢复 | 将对象复制到新卷、元数据 load 到全新 Redis，以新的空缓存 gateway 启动；全部 8 MiB 对象 SHA-256 一致。对象复制、元数据 load、gateway 就绪及 Go 测试执行合计 2.966 s |

TLS 探针启用 `-race`；最终增量检查 `gorgeous -d deploy/acceptance/deployment_test.go`、`go vet ./deploy/acceptance`、Python `py_compile` 均通过。默认未提供验收环境时，两个探针均明确 skip，已实际验证。JuiceFS gateway 未启用 writeback，成功上传已落底层对象；源 gateway 停止后才备份。恢复保留原卷 UUID 与 `/data` 的路径映射，但数据来自独立复制卷，恢复进程不挂载源卷，不复用源缓存。备份/恢复目录、临时密钥、一次性 PostgreSQL/Redis/gateway、网络和数据卷在脚本结束清理。

## 复现范围

脚本和要求见 [隔离验收说明](../deploy/acceptance/README.md)。普通 `go test ./...` 会跳过这两个外部环境探针，必须由脚本启动隔离依赖后执行；跳过不计为验收成功。

首轮发现 OrbStack `--internal` 网络不发布动态宿主端口，改用独占 bridge 和 loopback 端口。随后修复脚本重复传入 stdout 的错误，两次失败均清理独占资源；未修改业务实现或现有服务。

## 上线前仍需给定的条件

1. 部署域名、入口与内部地址、CA/证书来源、密钥注入方式。当前 TLS 探针显式注入 Go transport 信任根，尚未验收生产证书、镜像系统 CA 挂载、真实 StorageInternal 证书文件装配或跨容器业务通信。密钥三阶段是顺序重装配验证，没有证明多副本滚动期间零中断；证书自身的轮换需以最终部署设施另行演练。
2. 预计对象大小、对象总数、业务 QPS、并发上传数、保留期和目标尾延迟。8 MiB 本机样本证明恢复后的字节正确，不能据此确定生产容量、吞吐或磁盘配额。
3. RPO（可接受数据损失时长）、RTO（可接受恢复时长）、独立备份目的地及保留策略。本轮冻结快照没有后续写入丢失；0.628 s / 2.966 s 是这次小数据演练耗时，不是生产承诺。
4. 两业务库、JuiceFS 元数据、底层对象和密钥的联合恢复点演练。既有 [业务双库恢复](t12-backup-restore-20261015.md) 与本轮 JuiceFS 恢复分别通过，但尚未将四层作为同一组快照联合恢复并走真实身份的业务请求。

上述输入到位后，按最终环境部署并验收，不从开发环境推断生产值。
