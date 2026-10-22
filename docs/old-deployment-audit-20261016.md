# OrbStack 旧实例清理只读审计

审计时间：2026-10-16 22:56（Asia/Shanghai）。context：`orbstack`。本次仅查询资源、配置引用和数据库，没有修改或删除集群资源，不含凭据。

> 历史记录：本文比较的两套环境均已于 2026-10-22 退役。以下资源保留要求和审计结论只描述 2026-10-16 的阶段状态，不再是当前运维指令；最终清理见 [新部署验收记录](orbstack-rebuild-acceptance-20261020.md)。

## 当时结论

**初次只读审计时不能直接删除旧链路；后续正常 API 回收及双库恢复验证已完成，详见文末。** 新前端已经使用 已退役独立应用环境的 `artifact`，但旧 storage 库仍持有一条尚未到期的上传准备记录，且旧库中有未迁移的 JSON 验收数据。应保留旧服务履行回收义务，或另行验证能够承接该义务的维护方案；不得为清理实例直接改数据库期限。

## 当时流量及引用

- 前端实际 `forge-tenant-web-nginx/default.conf` 的 `/artifact/v1/` 和 `/storage/v1/` 都代理到 `http://artifact/v1/`，当时新入口位于现已退役的独立应用环境。
- 新 artifact 的 Forge scope resolver 指向已退役独立应用环境的 tenant HTTPS 服务（8443 端口），不依赖单独的 `forge-tenant-tls`。
- tenant worker 的 envFrom 顺序为 `forge-tenant-env`、`tenant-storage-integration`，后者把 STORAGE_URL 覆盖到新 artifact TLS 地址。
- tenant API 仍只读取 `forge-tenant-env`，其中 STORAGE_URL 保留旧 artifact 地址。即使 API 不运行 bucket worker，也应在确认新配置持久化后修正这条陈旧引用。
- 旧 artifact resolver 依赖 已退役独立应用环境的 `forge-tenant-tls`；旧 artifact 使用 `artifact-storage/artifact-forge-postgres`。两者构成仍在运行的旧链路。
- 旧服务仍有各 2 条 artifact/storage 数据库连接。没有将历史 last-applied 注解中的旧字符串认定为当前真实流量。

## 两套元数据快照

| 数据 | 旧 artifact-storage/artifact-forge-postgres | 当时新库 artifact-postgres（环境已退役） |
| --- | --- | --- |
| artifacts | 4 active | 2 archived |
| versions | 4 ready | 3 deleted |
| version_files / draft_files | 均 0 | 均 0 |
| drafts | 0 | 0 |
| artifact 后台任务 | 0 | 3 reference-release done |
| reference_changes | 0 | 3 committed |
| storage_files | 1 uploading | 1 deleted |
| storage_references | 0 | 3 released |
| storage_tasks | 0 | 1 delete done |

旧 4 个物料名称为 `browser-tenant-*`、`browser-project-*`，属于此前浏览器验收，均为独有记录，不能把新库视为旧库副本。保留双库 dump 和原 PVC 可保存这些历史证据。

旧 uploading 记录 `a240c731-ef79-49f4-aa44-9e933953eb22`：expires_at 为 **2026-10-17 22:20:17.290987（Asia/Shanghai）**；last_grant_expires_at 为 NULL；尚无引用和后台任务。NULL 表明库中未记录已签发凭据，不能据此替代对象端核查，也不能声称回收已经完成。当前没有更改该期限、读取或删除对象。

## 清理范围与保留范围

初次审计时可立即删除的 Deployment/Service：**无**。以下为义务完成、陈旧引用清理及备份核验后的候选清单：

- `artifact-storage`: Deployment/Service `artifact-forge`。
- `artifact-storage`: Deployment/Service `artifact-forge-postgres`。
- 已退役独立应用环境：Deployment/Service `forge-tenant-tls`（当时须先确认没有旧 artifact resolver 继续调用）。

不在删除范围：

- PVC `artifact-storage/artifact-forge-postgres`，以及相关 Secret `artifact-forge-postgres`、`artifact-forge-config`、`artifact-forge-tls`。
- 已退役独立应用环境的 Secret `forge-tenant-tls-tls`，当时在回退保留期内保留。
- 所有新 artifact、tenant、前端资源及 `artifact-integration-db` PVC。
- `artifact-storage/storage`、Service `juicefs`、PVC `storage-data` 及其凭据。它们仍承载新服务使用的 JuiceFS 对象存储，绝不能随旧 artifact 清理。

执行候选清理前，应保存私有 0700 目录内的旧 artifact/storage `pg_dump`、部署与 Service 清单、相关 Secret，并验证备份可读取/恢复；保留原 PVC。随后复查旧文件已到达可维护的终态、所有回收任务完成、无活跃引用。因保守清扫还会检查终态位置，停旧 worker 前也要确认无旧写入入口和仍有效写凭据。本文不代表已完成备份、迁移或删除。

## 后续正常 API 回收和备份验证

2026-10-16 23:00（Asia/Shanghai）复查：通过独立无头浏览器真实 Casdoor 登录，向旧服务正常 `DELETE /v1/storage/files/a240c731-ef79-49f4-aa44-9e933953eb22` 请求携带原始 Origin/CSRF 和正确租户范围，返回 **202**。旧 worker 随后完成回收，数据库确认 **file=deleted，delete task=done**。没有直接修改数据库、缩短期限或强制对象删除。

旧 artifact/storage 两库 `pg_dump -Fc` 保存在忽略目录 `.scratch/forge-orbstack/private/retired-backup/`（目录 0700、文件 0600）。在独占、无网络的临时 PostgreSQL 17 容器中恢复，两库所有业务表逐表行数与源库一致：artifact 仍有 4 物料、4 版本、4 幂等请求；storage 有 1 deleted 文件、0 引用、1 done 任务。恢复验证容器及其临时卷已删除，备份和校验记录保留。

- artifact dump SHA-256：`ebdc032853e2ff3487b1d39419131e50dd2a307bb43bdf41f17b7f9fb5e846b9`
- storage dump SHA-256：`82fc92a1ca0821dbd46541c8b5aa6dadaad7fd383c5df6c0842aa2091482e113`

这消除了旧上传准备记录的回收阻塞。候选 Deployment/Service 在主任务确认新部署配置稳定、陈旧引用已修正、回退资源已保存后可以退役；旧 PVC/Secrets 与备份仍保留。审计代理没有删除或缩容任何集群实例。

## 最终退役

主任务在完成陈旧引用修正、两次模板部署及旧实例缩容后的真实文件上传/下载验收后，删除了上述六个候选 Deployment/Service。删除后第三次整套重部署成功，同一已发布版本与文件字节仍一致。旧PVC/Secrets和恢复备份继续保留，共享JuiceFS未受影响。恢复清单为私有 `retired-backup/restore-resources.json`。详见[配置收敛记录](deployment-consolidation-20261016.md)。
