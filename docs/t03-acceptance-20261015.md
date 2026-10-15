# T03 alias 与版本管理验收

日期：2026-10-15。范围仅 T03，T04–T12 未开始；未提交、推送或发布。

## 交付

新增 alias PUT/GET/DELETE、版本 annotations/protection PATCH 及五个 Go SDK 方法，累计 12 个业务 HTTP 操作，OpenAPI 同步生成。

alias 使用规范化 ASCII 名称、修订 CAS 和删除墓碑。首次绑定预期修订为 0，墓碑重建沿用当前修订；latest 动态读取最高 Sequence 的 ready 版本，不持久化且不可写。数据库约束保证 alias 及目标属于同一集合。

直接发布支持初始 protected 与 alias 列表；共享 alias 变更规则参与发布事务，失败不消耗编号或留下部分 alias。成功重放先查询回执，不重做 alias CAS，也不覆盖后续保护变化。请求指纹包含规范化后的 alias 与保护参数，alias 顺序不影响重放。

注释完整替换与保护更新共享 VersionRevision，保留 payload。显式 false 有效；归档集合与已授权历史 scope 允许管理既有版本。管理提交结果不明返回 503 OperationOutcomeUnknown，调用方读取当前状态确认，不盲目重试。

## 审查与修正

实际启动 3 个子代理，分别完成 alias、版本管理、SDK/测试工作；其中两位随后分别执行 Standards 与 Spec 只读审查，均已完成。主代理负责装配、集成与验收。

Spec 审查发现版本管理将 expectedRevision=0 判为 400，与设计和 SDK 的非负整数契约不一致。已改为走 CAS 并返回 409 RevisionConflict，两个 PATCH 均增加双库回归用例。缺失或负数仍为 400。Standards 审查未发现其他确定问题。

## 验证

最终完整检查通过（退出码 0）。运行 `GOCACHE=/private/tmp/artifact-go-build make check`，使用隔离的 PostgreSQL 17 与 MySQL 8.4：HTTP/SDK、alias 并发 CAS、墓碑恢复、latest、发布冲突回滚、重放不恢复旧 alias/保护、注释与保护共享修订、scope 隔离和大小限制，以及已有 T01/T02 回归。检查包含 race、vet、build、格式与 OpenAPI 生成一致性；真实双进程测试覆盖既有发布编号与幂等行为，不据此声称新增 alias 独立进程场景已专项验收。

生产身份 resolver 尚未接入，远端 CI 未运行。自动保留、删除与解除 alias 属于 T04；草稿、文件和 storage 仍待后续任务。测试通过不代表这些能力已交付。
