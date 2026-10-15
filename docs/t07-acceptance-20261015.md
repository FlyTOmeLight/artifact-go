# T07 storage 文件生命周期验收

日期：2026-10-15。范围仅T07，T08–T12未开始；未提交、推送或部署业务服务。

## 实现

新增登记、查询、确认、删除四个公开HTTP操作及storagev1 SDK。登记和确认请求与200响应直接使用顶层数组，1–1000项；先检查结构和重复，再按输入顺序独立执行。成功项携带文件字段及error:null，失败项只含关联标识和结构化错误。用户身份错误整体拒绝，SDK整体成功不代表所有项成功。

登记保存精确scope、用户、requestKey和参数指纹，按固定tenant bucket分配独占key；不自动创建bucket。同键同参返回原FileID和当前状态，不延长期限、不复活终态；历史scope可确认已成功登记。size支持零字节，contentType规范化，可选声明摘要只保存客户端声明。确认前size为null；确认只核对对象存在和大小，Stat后重新锁文件检查状态、期限和已保存BackendID。ready/bound重复确认不再次读字节。

删除未引用文件原子进入deleting并写任务，首次/进行中202，deleted重复200，bound拒绝409。本轮没有创建引用的接口，StorageReference和Bind/Release仍由T09交付。文件位置唯一且永不复用，deleted保留位置及幂等墓碑。

storage独立拥有storage_files和storage_tasks。NewStorage/BuildStorageWithVerifiers支持仅storage部署；combined显式传入StorageDB，使用独立连接及迁移开关。worker由Start启动、Stop取消并等待，借用数据库不关闭；不读取artifact表或增加跨域外键。配置样例已补齐。

worker按数据库时间扫描过期uploading/ready，独立领取任务、续租及检查token。物理I/O在事务外，先等待LastGrantExpiresAt，再枚举并中止精确key的multipart，然后Delete；完成时短事务再次核验租约。墓碑周期检测迟到对象/分片，发现残留或探测失败进入有界删除任务；failed不会被扫描自动恢复。未知key和prefix相邻key不会被清理。清理完成表示S3不可读，不代表JuiceFS trash磁盘块立即释放。

maintenance增加storage域，默认dry-run；按expectedRevision重排failed任务，保留义务、NotBefore和累计次数，审计与重排同事务。任务没有公开HTTP管理接口。

## Standards review

最终冻结生产面审查未发现遗留问题。三个子代理分别负责文件业务/adapter、回收worker/维护、SDK/公开HTTP测试；其中两位再独立承担Standards和Spec复核，均已完成。主线程负责公开装配、配置、生成物与集成验证。

## Spec review与修正

最终未发现T07遗漏或后续任务越界。本轮修正：

- MySQL首次登记响应采用数据库实际持久化时间，保证重放期限完全一致。
- 批量入口在逐项执行前验证可信scope，身份错误不会包装成200逐项失败。
- 确认/回收检查登记时BackendID，避免切换配置后访问或误删另一后端。
- 物理位置增加联合唯一约束；声明摘要按设计分三项可空字段保存。
- 对象操作限定总超时并检测multipart分页循环；明确不存在按幂等清理处理，权限失败仍失败。
- 墓碑探测故障进入失败预算，不因单个异常位置阻断后续文件，也不无限复活failed任务。

## 验证

完整命令：`GOCACHE=/private/tmp/artifact-go-build make check`。最终检查通过，退出码0；双库集成测试74.7秒。

覆盖真实PostgreSQL 17/MySQL 8.4的HTTP生命周期、混合逐项结果、输入结构、同键并发、历史scope、确认期间删除/到期、登记/确认/删除COMMIT回执丢失、过期回收、LastGrantExpiresAt等待、失租接管及旧token、长I/O续租、失败预算/审计恢复、两个独立进程竞争、维护CLI、独立storage表范围及借用DB生命周期；同时执行T01–T06回归、race、vet、构建、格式及OpenAPI一致性。当前共25个业务HTTP操作。

真实JuiceFS通过环境门控的TestStorageFileLive单独验证，两种数据库均通过：公开HTTP创建测试bucket/登记，使用管理S3身份PUT，再确认、删除并由worker完成HEAD404；同key未完成multipart被中止，prefix邻居保留；迟到PUT被墓碑扫描再次删除，文件仍为deleted。该验证不包含grants，不代表T08客户端授权已交付。测试随机bucket、对象、分片、专用数据库容器和端口转发均已清理。

JuiceFS凭据通过私有环境注入，未写入代码、参数或报告；后端命名空间与管理凭据边界沿用[T06探针记录](t06-storage-probe-20261015.md)。真实生产身份接入、远端CI及T08之后能力仍待对应切片验收。
