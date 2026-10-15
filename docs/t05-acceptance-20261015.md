# T05 纯 JSON 草稿验收

日期：2026-10-15。范围仅 T05，T06–T12 未开始；未提交、推送或部署。

## 实现

新增草稿创建、摘要列表、读取、完整替换保存和条件删除五个 HTTP/SDK 操作；发布接口支持指定 draftID 与 expectedDraftRevision。每个集合支持多份独立草稿，不分配版本编号，不参与 latest、alias 或自动保留。非空 files 仍拒绝，未提前实现 storage 或文件交接。

创建使用 DraftCreateRequest 保存成功身份；保存使用 ReferenceChange 保存固定请求指纹、appliedRevision 和空引用计划。成功重放先于 active、归档、来源存在、当前修订和字段大小限制检查。保存响应区分本次 appliedRevision 与当前完整 draft；硬删后创建/保存重放返回 410 DraftDeleted，保留身份及保存的 appliedRevision。请求键按集合、可信用户及操作隔离。

草稿读写按 Artifact → Draft 锁顺序返回一致快照，列表只返回摘要并绑定 scope/集合游标。保存需要 requestKey、正数 expectedRevision、完整 payload/annotations/files；files:[] 必须明确传入。冲突先持久写入 aborted 空计划再返回 409，改变同一请求键的参数返回冲突。没有外部引用时，在同一本地事务中直接提交最终决定，不模拟文件准备阶段。

从版本复制按原始 sourceVersionID 计算指纹，来源须在同集合且 ready。草稿发布以原始 DraftID/Revision 和发布参数计算指纹，成功后不修改或删除草稿。草稿编辑或硬删不破坏原发布重放。发布继续遵守既有 alias、保护和 retention 的原子事务。

提交结果不明时有界读取主库回执及一致草稿快照，不能盲目再次保存。只有确认本次 AttemptID 已 aborted 才返回确定的修订冲突；不能确认则 503。

## Standards review

发现并修复 1 项：删除接口提交结果不明时错误提示要求使用不存在的 requestKey。现提示读取草稿状态，再按原 expectedRevision 核对；未新增删除回执。

## Spec review

发现并修复 1 项：初版保存 CAS 冲突直接回滚返回 409，未持久记录 aborted。现将空计划的中止决定提交后返回，补充同键异参、同参后续重试及提交回执丢失的双库测试。另一位代理复查事务和错误处理，未发现残留问题。

实际启动 3 个子代理，分别负责草稿服务、发布接入、SDK/测试，均已完成；主线程负责模型迁移、装配、补充并发/故障测试和集成验收。

## 验证

完整命令：`GOCACHE=/private/tmp/artifact-go-build make check`。状态：完整检查通过，退出码 0；OpenAPI 共 20 个业务操作。

测试覆盖真实 PostgreSQL 17/MySQL 8.4、两独立进程 CAS、并发快照读取、创建/保存丢失 COMMIT 回执、持久中止、硬删 410、历史 scope、来源复制/发布、字段与整包限制、请求键作用域，以及既有 T01–T04 回归、SDK wire、race、vet、build、格式及 OpenAPI 一致性。

本轮不交付 Web 编辑器、带文件草稿、storage 或跨服务引用恢复。生产身份 resolver 与远端 CI 仍待验收。
