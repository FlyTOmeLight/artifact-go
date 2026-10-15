# T09 持久引用交接与单文件发布验收

日期：2026-10-15。范围仅 T09；T10–T12 未开始，未提交、推送或部署业务服务。

## 实现

storage 新增 storage_references，以 ConsumerID + ReferenceID 为固定身份；FileID 和精确 scope 不可改。引用只从 active 进入 released；先收到 Release 时写永久墓碑，迟到 Bind 不复活，也不会误删原本没有被引用的准备文件。批次按 FileID 排序锁，单批原子。非最后引用释放稳定完成；最后释放进入 deleting 并创建 storage 回收任务，等待一次物理删除回执，后续墓碑扫描不撤销该完成结果。

四个内部 HTTP/JSON 操作为元数据查询、Bind、Release、状态查询。请求包含精确 scope 和数组，成功直接返回同序数组；只在独立内部监听注册。服务 Bearer 映射稳定 consumer 和精确授权 scope，不接受业务 owner、用户 token、调用方 tx 或物理对象位置。多个凭据可映射同一 consumer；非回环要求 TLS。客户端限制连接 1 秒、单次调用 5 秒、最多 3 次并受调用方截止时间约束，处理 Retry-After 和结果未知，不盲目把断线当成未提交。

artifact 新增 VersionFile，保存逻辑路径、FileID、独立 ReferenceID 和确认元数据。单文件直接 CreateVersion 先持久保存 ReferenceChange 的 attempt 与完整新增引用计划，再通过 HTTP Bind，最后在 artifact 本地事务提交版本、编号、alias、retention 及成功回执。两个域没有共享事务或跨域外键。读取和成功重放返回一致的版本与文件快照。

attempt 从准备阶段起固定 120 秒，不续期；已有有效 attempt 优先返回 RequestInProgress。最终提交再次检查当前 AttemptID、preparing 状态、数据库期限及可信身份。请求失败先持久确认 aborted，完整补偿计划进入不可变任务载荷；结果未知只查询 artifact 主库决策，不根据 storage 状态猜测业务是否成功。恢复 worker 只中止、释放、收尾，不后台发布。

每项计划在 Bind 前保存原始准备期限，失败补偿不早于该期限；本轮任务取整份计划最晚期限，允许保留更久。内部计划按 1000 项分批，部分成功仍补偿全部引用，包括未发出及结果未知项。新 attempt 使用全新 ReferenceID，旧任务不会读取新计划。

版本删除及 retention 淘汰同事务保存释放义务，逻辑不可见后异步发送 Release；非最后引用可直接收尾，最后引用等待 storage 的物理删除回执。沿用有界任务失败预算与受控 maintenance 重排。T09 仅开放单文件直接发布；带文件草稿、多文件完整业务及复制由 T10 完成。从带文件版本创建草稿暂时明确拒绝，避免静默丢文件。

## Standards review

独立审查发现并修正 1 项错误语义问题：发布层现在保留内部客户端 OutcomeUnknown 标记，畸形 4xx 不再包装为确定业务拒绝；已通过公开 HTTP 双库 race 回归。其余模块职责、事务、资源所有权及约束未发现需修正问题，最终增量复核通过，0 项遗留。

## Spec review

独立审查冻结的 89 个生产文件，最终未发现 T09 遗漏、越界或契约缺陷。按实施切片将草稿及完整多文件入口留在 T10，不将全系统 Spec 当作本轮全部实现范围。

修正的具体问题：

- 正在执行的同键请求，先判断持久 attempt，再决定是否查询 storage，避免存储断线掩盖 RequestInProgress。
- metadata 查询消耗同一个 120 秒执行预算，不在查询后重置期限。
- 持久计划保存每项原始期限并支持分批，部分绑定失败时覆盖全部计划引用。
- 内部监听关闭连接后继续等待 handler 退出，避免数据库资源先于请求结束关闭。
- 新增 VersionFile 后，版本读取/成功重放在一致快照中读取清单；未实施的带文件复制明确拒绝。
- 未确认就被删除的文件，其 deleting/deleted 元数据保留 size:null；客户端只对 ready/bound 强制确认大小，避免拒绝合法墓碑查询。
- Consumer、scope 与 ID 输入长度受约束；不完整或畸形远程回执按结果未知处理，发布层保留该标记并返回 503，不误认成功或确定拒绝。

## 验证

`GOCACHE=/private/tmp/artifact-go-build make check` 退出码 0：PostgreSQL 17/MySQL 8.4 集成测试 83.644 秒，SDK、unit、race、vet、两个命令构建、gorgeous 格式和 OpenAPI 重生成一致性全部通过。公开业务操作仍为 26 个，内部四操作不进入公开 OpenAPI。删除态 nullable size 校验已包含在该次全量检查；之后仅新增 5 项明确回归，全部 TestInternalStorage* 共 20 项 race 测试、相关 vet 和局部格式检查通过，生产文件未再变化。

定向真实 PostgreSQL/MySQL race 测试已通过：storage 引用生命周期、先 Release 后 Bind、跨 scope/consumer、凭据轮换、同 ReferenceID 异文件竞争、批原子、未知 COMMIT、最后 Release 与新 Bind 竞争、LastGrantExpiresAt 等待、实际 worker 删除后的稳定回执；artifact 单文件发布/重放/Get/List、并发请求、过期旧 attempt、失败保留窗口、未知提交及 1001 项部分成功补偿。

独立进程验收在 PostgreSQL、MySQL 分别创建两个独立 database，并运行独立 artifact/storage 进程。通过公开 CreateVersion 进入真实业务链，代理在首个 Bind 已提交后丢弃成功响应；客户端重试后仍只有一份有效引用，用户同键重放得到同一版本。检查两域表隔离及轮换凭据映射同一 consumer。S3 使用受控外部适配边界，本轮没有重跑 T08 的真实 JuiceFS STS 到期测试。

combined 装配验证内部监听启动、普通用户拒绝、旧/新服务凭据、内部路由不出现在公开监听及 Stop 关闭连接；完整消费测试 HTTP 响应后连续三轮双库 race 通过。另用受控数据库回调延迟请求退出，验证 Shutdown 超时关闭连接后，Stop 仍等待 handler 完成再返回；两种真实数据库 race 均通过。生产 TLS 证书和真实服务凭据部署轮换仍归 T12，当前验证的是配置约束及协议身份轮换。

本轮实际启动 3 个子代理，分别负责 storage 引用、artifact 发布与恢复、内部传输及独立进程测试；其中两位另行执行最终 Spec/Standards 审查。主线程负责 service 配置与监听生命周期、schema 接线、集成验证和交付记录。

最终生产文件与审查快照一致。验收使用受控身份和临时数据库，不代表真实生产身份接入或远端 CI 已通过。

本轮临时数据库容器、数据库环境文件、审查快照和临时日志已清理；未提交或推送代码。
