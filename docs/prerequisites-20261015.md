# 前置任务进度（2026-10-15）

server-go 事务前置已完成；对象存储环境已部署。按用户最新决策，发布后的对象修改由客户端负责，取消服务端防覆盖及复制封存要求，并采用一租户一个 bucket。JSON 快照和数据库文件清单仍不可修改。无文件版本业务可以使用正式框架能力开始实现；文件上传仍需完成权限隔离、对象存在/大小核验及回收验收。

> 历史进度记录：下文存储部署已退役，状态与结果均以 2026-10-15 为准。当前安装与访问见 [Forge 部署说明](../deploy/forge/README.md)。

## 框架事务：已完成

- [PR #16](https://github.com/FlyTOmeLight/server-go/pull/16) 已合入 main，提交 `d61d3a6a12bc52386ffe8ac08ba7f8f4ef081f02`。
- [server-go v0.10.0](https://github.com/FlyTOmeLight/server-go/releases/tag/v0.10.0) 为正式发布，标签指向上述通过 main CI 的提交。
- 公开入口 `db.Transaction` 提供显式隔离级别、只读模式、取消及次数/时间受限的整事务重试。默认不重试；只重试明确终止事务的 PostgreSQL 序列化失败/死锁和 MySQL 死锁。业务冲突、唯一冲突及未知提交结果不重试。
- 完整 `make check` 通过，涵盖真实 PostgreSQL 17/MySQL 8.4、竞态、四个独立模块，以及可运行示例与生成项目的 HTTP/SDK/CLI 请求和退出。最终隔离级别可见性补测也通过双库 race、vet 和格式检查。
- 两次 PR CI 与 [main CI](https://github.com/FlyTOmeLight/server-go/actions/runs/38594693319) 均通过。
- 发布后独立模块在 `GOWORK=off`、无 `replace` 的条件下实际下载 v0.10.0，构建运行并验证提交、回滚及取消。解析版本和校验值对应正式模块。

## 对象存储：已部署，契约未通过

已退役的本机 OrbStack 独立存储环境当时仅运行 JuiceFS 1.4.1 多 bucket gateway 和 Redis 7.4.2。按用户要求，旧测试卷已删除，不迁移数据；RustFS 容器、Service、原生 S3 转发和凭证字段已移除。

JuiceFS 使用 `file` 后端，数据块直接保存到本机 SSD 上的 10 GiB PVC，Redis AOF 与数据块分别持久化。`local-path` 的容量请求不是磁盘硬配额；JuiceFS 逻辑容量为 9 GiB，缓存为 256 MiB。

当时的 S3 入口为 `http://127.0.0.1:19010`，region `us-east-1`，path-style。历史配置见 [退役环境部署记录](../deploy/storage/verification-20261015.md)，不再作为部署或复用入口。

[原部署契约报告](storage-contract/results-20261015.md)保留为历史能力证据。用户已取消发布后防覆盖要求，并否决暂存区复制到最终位置的封存方案；服务不因发布撤销已签发上传凭证。版本发布冻结数据库清单；旧上传会话与 sealed 状态已移除，固定引用保证版本及文件记录身份，不保证外部文件字节持续不变。artifact继续决定版本保留与逻辑删除；storage负责临时文件过期和物理回收，双方按独立事务持久交接，见[当前设计](version-service-technical-design-20261013.md)。

后续每租户独立 bucket，同租户各项目共享；服务按可信 TenantID 解析 bucket，客户端不能自行选择。当前两个本地测试 bucket 尚未关联真实租户，不代表真实租户空间创建接口已经实现。

取消条件写要求不等于直传契约已经通过：摘要仅记录客户端声明，原可信摘要失败不再是当前阻塞；普通 IAM 用户的 STS、精确 key 和跨租户 bucket 隔离、凭证到期及回收能力仍需验收。此前 root STS 403 不代表所有身份不支持 STS；[官方说明](https://juicefs.com/docs/community/guide/gateway/#assumerole-security-token-service)明确 root 不可签发，普通 IAM 用户可以使用 AssumeRole。探针和当前验收说明见 [直传契约验证](storage-contract/README.md)，历史结果保持原样。

本次没有创建 artifact-go 业务服务、SDK 或数据库模型；这些属于后续任务。

在本轮grill之前按当时契约重跑的JuiceFS数据探针：[机器报告](storage-contract/juicefs-client-managed-report.json)。防覆盖要求已取消；上传及清理通过，当时可信摘要及versioning/lifecycle查询失败；摘要已不属于当前发布门槛，跨租户隔离与普通 IAM STS 尚待验证。

最新grill决定：不独立验证实际文件摘要，只确认对象存在和实际大小；原机器报告的摘要失败保留历史事实，不用于当前验收。声明字段标为declaredChecksum。当前探针尚需在后续实施时适配此契约，普通IAM/跨桶/到期及回收仍待验证。

## T00 本轮复核

2026-10-15的新调查、配置建议与实测见[T00 review报告](t00-review-20261015.md)。普通IAM精确多key授权已有本轮正反例，后端最短STS期限900秒、session policy长度上限2048字符已实际观测；完整到期、清理和未验收项以[T00存储报告](storage-contract/t00-juicefs-report.md)为准。上述历史探针结果不改写。HTTP/JSON内部交接已确认，具体路由、接入及开发默认值已获用户批准；仅开始T01，后续能力按切片验收。
