# 本机四层联合恢复验收

日期：2026-10-16。结果：隔离环境实际通过，包含 artifactDB、storageDB、JuiceFS 元数据和对象数据在同一冻结点的备份与恢复。没有修改生产服务、现有联调实例或数据库期限。

## 运行方法与范围

```sh
python3 deploy/acceptance/four_layer.py
```

脚本要求本机已有 `postgres:17-alpine`、`redis:7.4.2-alpine`、`busybox:1.37.0` 和 `artifact-juicefs:1.4.1-arm64` 镜像，使用 `--pull=never`；不会在默认 CI 或 `make check` 中自动运行。`TestFourLayerRecovery` 未设置专用 phase 时跳过。

每次创建新 UUID 命名的 Docker 网络、源/恢复 PostgreSQL、源/恢复 Redis、源/恢复 JuiceFS gateway、两个对象卷与一个备份卷。每个 PostgreSQL 容器各含独立 artifact/storage 数据库。只发布回环地址端口；测试凭据本轮随机生成，保存于 0700 临时目录中的 0600 文件，不加载任何现有集群凭据。测试配置和随机凭据保留至本次恢复结束，随后随临时目录清理。

JuiceFS 使用真实 S3 gateway，底层对象块存储为独占文件卷。恢复使用新 PostgreSQL、新 Redis、新对象卷、新 gateway，gateway 缓存设为 0。源 PostgreSQL、Redis 和 gateway 在恢复前全部停止；恢复阶段包括 JuiceFS load 在内的所有容器都不挂载源对象卷，只使用备份卷和恢复卷。恢复请求无法使用源服务、源对象卷或其内存缓存。

## 实际业务与冻结点

1. 使用生产 `StorageService`、artifact `Service`、HTTPHost 和受服务身份保护的 storagehandoff handler；显式测试身份仅指定本次隔离租户。
2. 经公开 storage HTTP 开通 bucket，登记两个文件，测试 S3 操作者将非零确定内容直接 PUT 到登记的位置，再经公开 HTTP Complete 确认。
3. 经公开 artifact HTTP 发布两个版本：保留版本引用 shared 文件；待删版本同时引用 shared 和 unique 文件。随后通过公开 DeleteVersion 创建持久删除义务。
4. 源服务不启动后台 worker，以确定保留可恢复的在途义务；冻结前断言版本处于 deleting、存在未完成 reference-release 任务、storage 中有三条 active 引用。Go 测试退出时关闭并等待所有 HTTP handler、连接及资源清理，之后脚本停止源 gateway。
5. 所有业务写入者和 gateway 均停止后，顺序执行两库 `pg_dump --format=custom`、JuiceFS `dump` 和对象卷复制。因为冻结后没有写入，这四份备份对应同一业务状态，而非四个仍在变化的快照。

shared 文件为 1,081,344 字节，unique 文件为 950,272 字节，合计 2,031,616 字节。文件准备期限在构造服务时设置为 30 秒；未修改数据库中的 expires_at、任务期限或租约时间。没有签发 STS 凭据，因此没有待等待的上传授权有效期；此演练不验证上传授权路径。

## 恢复后的断言

- 两业务数据库均由 dump 恢复到全新 PostgreSQL 数据库，JuiceFS 元数据 load 到全新 Redis，对象复制到新卷。
- 启动恢复后的 worker 前，确认原版本 ID、manifest 文件 ID、payload、deleting 状态、未完成义务及三条 active 引用已还原。
- 经公开 artifact HTTP 读取原保留版本，经恢复 gateway 读取 shared 与 unique 两个对象，并逐一比较 SHA-256；待删对象先确认字节真实存在，避免对空位置执行删除也通过。
- 启动恢复后的生产 artifact/storage worker，按现有持久任务推进，不插入伪造完成记录、不改变期限。
- 待删版本最终为 deleted，unique 文件为 deleted，storage 删除任务有 done 回执；artifact 的 reference-release 义务全部 done。
- storage 保留两条 released 引用墓碑，只剩一条归属于 `four-artifact` 的 shared active 引用；shared 文件保持 bound。
- unique 对象 Head 返回明确 NotFound/NoSuchKey；shared 保留版本仍 ready，最后再次读取并验证 shared 字节。

## 实测结果

最终命令运行的 seed 与 restore 两阶段均使用 `go test -race -count=1`，实际通过：

| 项目 | 结果 |
| --- | --- |
| seed：公开文件/版本请求与冻结前义务断言 | PASS，测试主体 0.21 秒 |
| 冻结后四层备份 | 0.638 秒 |
| restore：新环境、原始字节与恢复任务断言 | PASS，测试主体 1.16 秒 |
| 新环境启动、恢复及业务校验总时间 | 8.321 秒 |
| 准备期限或任务期限的数据库改写 | 无 |

以上时间仅描述这次约 2 MiB 数据的本机演练，不是生产容量、RPO 或 RTO 指标。该测试使用显式可信身份，不声称覆盖真实 Forge 登录；S3 PUT 使用隔离测试操作者，不声称覆盖 STS、浏览器上传或授权到期。

脚本在成功或失败时都会清理本次 UUID 命名容器、卷和网络；数据库 dump、状态清单、临时配置和随机凭据随临时目录删除。本轮最终运行后，已只读查询 Docker 的 `artifact-four-` 名称前缀；容器、卷和网络结果均为空，确认没有本次演练遗留资源。
