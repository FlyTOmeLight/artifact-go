# T12 两域数据库备份恢复验收

本次验证 PostgreSQL 17 与 MySQL 8.4 上 artifact、storage 两个业务域数据库的成对备份恢复。测试通过真实 `pg_dump` / `psql` 和 `mysqldump` / `mysql` 导出、还原；没有用 ORM 复制数据替代备份工具。

## 验证过程

`tests/integration/backup_restore_test.go` 的 `TestPairedDatabaseBackupRestore` 对每个引擎：

1. 创建独立 artifact、storage 测试数据库，通过 HTTP 发布共享文件的两个版本；删除其中一个，保留待执行的释放义务。
2. 准备已绑定但未提交的 preparing 决策、已 aborted 决策及完整计划。每个计划同时包含已发送和未发送的引用，模拟进程在交接中退出。
3. 停止接受 artifact HTTP 写入；没有启动后台 worker。冻结两域写入后分别备份。这是受控停止写入的一致性策略，不声称两个独立运行中的数据库能自动获得同一瞬间的快照。
4. 创建另外两个空数据库，将两份真实 dump 还原到新位置。后续 API 与恢复 worker 只连接恢复后的数据库。
5. 验证成功发布的幂等回执、文件清单与 active 引用一致；扫描将过期 preparing 变为 aborted，不发布新版本；执行完整释放计划，为未发送项写稳定墓碑，完成原版本删除义务。
6. 验证成功版本仍持有唯一 active 引用，文件继续 bound；未完成释放任务为零，删除版本到达 deleted。

测试数据库名由 fixture 随机生成，dump/restore 入口只接受 `handoff_<12位随机十六进制>`。容器 ID 由检查脚本传入；不读取生产凭据，不向日志打印 dump 内容。测试结束删除源与恢复数据库，dump 只保存在测试进程内存。

## 执行和结果

运行 `make check` 时，`scripts/check.py` 启动一次性 PostgreSQL/MySQL 容器，并将两个容器 ID 传给测试。单独运行此测试但未提供检查脚本的容器信息时会明确 skip，不计为验收通过。

2026-10-15 定向验收：双库 `-race` 测试通过，总耗时 4.642 秒；PostgreSQL 1.60 秒、MySQL 1.12 秒。实际命令为 `go test -race ./tests/integration -run '^TestPairedDatabaseBackupRestore$' -count=1 -v`，连接参数只从本次隔离环境传入。

## 验证边界和部署要求

本记录证明两业务域决策、清单、引用和释放义务可成对恢复，不是完整存储灾备验收。测试使用受控文件元数据，没有备份或恢复 JuiceFS 的元数据库、底层对象块、缓存或实际文件字节；未测生产规模的恢复时间、可接受数据损失或在线备份。

实际部署须在冻结 artifact/storage 写入和两个域的 worker 后，保存对应的两域数据库备份，并为 JuiceFS 元数据及底层数据制定匹配的备份恢复策略。恢复时先保持写入和 worker 停止，验证业务清单对应的引用、准备计划、释放任务及 JuiceFS 数据位置完整，再启动恢复扫描。不能把不同时点的任意两份数据库备份混合后直接运行回收，也不能只还原 artifact 清单而忽略 storage 引用墓碑。

failed 任务的 maintenance CLI 人工重排验收另行记录；本测试通过正常 worker 恢复过期决策与 pending 释放义务，不将其冒充人工重排测试。
