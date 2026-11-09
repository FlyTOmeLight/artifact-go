# 状态迁移与巡检

状态迁移规则集中在 `internal/lifecycle`，由现有业务事务调用。数据库中的状态值、公开 API 和 SDK 保持原契约。初始化记录仍由各领域负责。

## 资源状态

| 对象 | 允许的迁移 | 事务内前提 |
| --- | --- | --- |
| StorageFile | uploading → ready | 锁定文件，复查准备期限及对象确认元数据 |
| StorageFile | ready → bound | 锁定文件，在原准备期限内成功创建 active 引用 |
| StorageFile | uploading / ready → deleting | 显式删除或准备期限到期，同时创建 delete 任务 |
| StorageFile | bound → deleting | 锁定文件，最后一份 active 引用已释放，同时创建 delete 任务 |
| StorageFile | deleting → deleted | 对象删除成功，最终租约校验通过，同时完成 delete 任务并安排 NextSweepAt |
| StorageFile | deleted → deleted | 墓碑复扫后的重复物理清理，文件身份不复活 |
| StorageReference | active → released | 锁定关联文件和引用；released 墓碑不可重新绑定 |
| Version | ready → deleting | 持有 Artifact 锁，清空文件清单并原子记录完整引用释放任务 |
| Version | ready → deleted | 无文件引用，直接在本地事务完成删除 |
| Version | deleting → deleted | 释放任务确认全部引用释放完成，最终租约校验通过 |
| ReferenceChange | preparing → committed | 同一 attempt 未到截止时间，业务写入与成功决定原子提交 |
| ReferenceChange | preparing → aborted | 中止决定与完整补偿释放任务原子提交 |

同一 ReferenceChange attempt 的 committed 和 aborted 互斥。重试 aborted 请求时必须使用新的 AttemptID，并保留旧补偿任务的不可变载荷；preparing 或 committed attempt 不能被新 attempt 覆盖。过期 preparing 必须先中止。纯 JSON 操作在一个本地事务内直接记录最终决定。

幂等请求在业务入口返回已有结果，无需重复迁移。文件的 deleted → deleted 用于墓碑复扫，是显式允许的重复终态写入。

## 任务状态与事件

BackgroundTask 和 StorageTask 共用任务迁移规则，各自保留领域事务、租约和审计逻辑。

| 事件 | 允许的迁移 | 调用方负责的条件 |
| --- | --- | --- |
| ClaimTask | pending / running → running | 已到执行时间，或旧租约过期；签发新 token |
| RetryTask | running → pending | 确认执行失败但预算未耗尽，或仍需等待授权到期、引用释放完成 |
| FailTask | running → failed | 已确认失败且失败预算耗尽 |
| CompleteTask | running → done | 最终 token 和租约校验通过 |
| ScheduleTask | done → pending | 新一轮 retention 评估或墓碑复扫；递增 RunGeneration |
| RecoverTask | failed → pending | 人工恢复，校验 expectedRevision，记录审计并递增 RunGeneration |

租约续期只更新截止时间和 Revision。过期接管不消耗失败预算。提交结果不明时沿用原有确认或租约恢复逻辑，不能当成已确认失败进行重试或补偿。

## 只读巡检

`cmd/maintenance --action doctor` 分别检查 artifact 或 storage 数据库，不修改数据，不调用对象存储。具体参数与输出说明见根目录 README 的维护命令。

巡检覆盖 bound 文件缺少 active 引用、deleted 文件的回收任务或复扫安排异常、deleting 版本缺少可继续执行的引用释放任务、已过期 preparing 决定，以及耗尽重试预算的 retention 任务。

过期 preparing 表示恢复工作已到期，可能处于正常扫描间隔内，不能单凭一次报告认定为数据损坏。墓碑有正常的 pending / running 复扫任务也不能误报为非法文件迁移；failed 任务仍需人工检查与恢复。
