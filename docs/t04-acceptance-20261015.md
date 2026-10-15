# T04 保留、删除与后台任务

日期：2026-10-15。范围仅 T04，T05–T12 未开始；未提交、推送或部署。

## 实现

新增完整替换保留策略、预览策略和删除版本三个 HTTP/SDK 操作，累计 15 个业务操作。GetArtifact、列表和归档响应返回真实策略；尚未配置时 revision=0。策略更新要求全部规则字段出现，两个上限允许显式 null；预览使用一致的锁内事实和数据库时间，不写资源。

retention.Plan 为纯函数，按数量/年龄、显式保护、alias、最新 minVersions 计算候选与原因。发布先模拟最终 alias 指向，在同一事务提交新版本、alias、编号、回执和旧版本淘汰；reject 或无法满足数量上限时整体回滚，新版不会在本次创建中被淘汰。年龄按后台扫描执行。

显式删除检查 Protected 和普通 alias，可删除最后一个无保护版本。纯 JSON 删除清空 payload/annotations，保存身份墓碑与原发布回执；重复 DELETE 返回墓碑，原请求重放 410。自动淘汰允许解除 alias 时复用 alias.Delete，修订递增一次且保留墓碑。

BackgroundTask 使用数据库时间、SKIP LOCKED、租约 token、续租和有界失败预算。每个实例的 worker 随公开 Service.Start/Stop 启动、取消并等待。已确认执行失败按退避重试；正常保护阻塞只记录结果，failed 不自动复活。执行提交结果不明不会记为确认失败，等待租约接管。

维护命令支持默认 dry-run、显式执行、修订 CAS、持久恢复审计。保留身份、载荷、NotBefore 和累计执行次数；未知提交通过主库审计确认，否则明确报告 OperationOutcomeUnknown。无公共 task API，未接入 storage 或文件释放。

## Review

3 个子代理完成策略、worker、SDK/测试分工；随后按 Standards、Spec 两轴审查，主线程集成验证。

### Standards

未发现需要扩大结构或引入抽象的确定问题。
### Spec

发现执行期间持任务行锁会阻塞续租。已改为先锁 Artifact 执行业务，提交前再锁任务、核验当前 token 与数据库期限；续租能在执行中独立提交，旧执行者失租后回滚业务。增加真实数据库屏障回归。

集成补查：恢复审计记录变更前 Revision，支持提交回执丢失后的确认；执行提交结果不明不消耗失败预算。维护 CLI 校验 MySQL UTC 配置并禁止自动迁移。真实命令测试另发现 SQL 日志混入 stdout，现显式绑定框架 logger 到 stderr，stdout只输出结果JSON。MySQL恢复测试改为精确比较已持久化的NotBefore，避免混用内存微秒与数据库毫秒精度。

## 验证

完整命令：`GOCACHE=/private/tmp/artifact-go-build make check`。验收状态：完整检查通过，退出码 0。

检查范围包括真实 PostgreSQL 17/MySQL 8.4 HTTP/SDK、策略与删除回归、执行中续租、失败预算、旧 token、独立进程争领/过期接管/竞争重排、恢复及执行的提交回执丢失、真实维护二进制 dry-run/执行、服务停止与借用数据库所有权，以及 race/vet/build、格式和 OpenAPI 生成一致性。

生产身份 resolver 与远端 CI 尚未验收；本轮没有实现草稿、storage、文件回收或跨服务引用释放。
