# JuiceFS 本地 SSD 部署（2026-10-15）

按用户要求，仅保留 JuiceFS 多 bucket gateway 和 Redis 元数据库。旧测试数据不迁移：原 Deployment、RustFS Service 和旧 PVC/PV 已删除，然后创建新的 10 GiB PVC。

## 当前结构

```text
S3 客户端 → http://127.0.0.1:19010 → JuiceFS gateway --multi-buckets
                                      ├─ file 后端 /data/ → SSD PVC
                                      └─ Redis → 同一 PVC 的独立目录
```

- Kubernetes context：`orbstack`；namespace：`artifact-storage`；Deployment：`storage`。
- Pod 只有 `juicefs`、`redis` 两个容器，实际 `2/2 Ready`；Service 只有 `juicefs`。
- JuiceFS 1.4.1，镜像 `artifact-juicefs:1.4.1-arm64`；Redis 7.4.2-alpine，AOF 开启。
- 新 PVC `storage-data`：10 GiB、RWO、`local-path`，绑定 `pvc-efb5bbb4-cbce-4e87-bdae-c81213e3d8ea`。
- 实际卷 UUID：`7b06f17f-87c9-4d0a-8c50-0e0ab40bd5b1`；Redis 中的卷配置确认 `Storage: file`、`Bucket: /data/`，数据块位于 `/data/artifact-jfs/chunks`。
- PVC 使用 `juicefs-data`、`redis`、`juicefs-cache` 三个 subPath。`local-path` 的 10 GiB 请求不是物理硬配额；JuiceFS 逻辑容量为 9 GiB，缓存为 256 MiB。
- 已创建两个空 bucket：`artifact-data`、`artifact-models`。多 bucket 映射到 JuiceFS 顶层目录，共享文件系统容量。
- 未配置训练节点 FUSE 挂载。采用本地 file 后端时，挂载客户端需要能访问同一数据目录与元数据库；远程机器仅连接 Redis 不足以访问数据。

## 使用

- S3 endpoint：`http://127.0.0.1:19010`；region：`us-east-1`；使用 path-style。
- 启动或重复部署：`deploy/storage/bootstrap.sh`。
- 本机端口转发：`deploy/storage/port-forward.sh`。Pod 重建后需重启转发；只监听 `127.0.0.1`。
- Kubernetes Secret：`artifact-storage/storage-credentials`。
- 本机凭证文件：`/private/tmp/artifact-storage/credentials.env`，权限 `0600`。保留 `MINIO_ROOT_USER`、`MINIO_ROOT_PASSWORD`；原 RustFS 的 `AWS_*` 字段已删除。
- 对应 S3 SDK 的 access key / secret key 分别取上述两个字段；凭证不进入仓库或命令行。此身份是 Gateway 管理员，限权临时上传凭证需要另外配置普通 IAM 用户。

## 验证结果

1. Kubernetes server dry-run 和脚本 `bash -n` 通过。
2. 新卷初始 bucket 列表为空，确认未复用原数据。
3. 创建两个 bucket，各写入测试 marker，GET 逐字节一致。
4. 真实完成 5 MiB + 1 MiB 两片上传，GET 与原始 6 MiB 内容逐字节一致。
5. 重建整个 Pod 后，两个 bucket 的 marker 均仍可读取且逐字节一致。
6. 测试对象已删除，两个 bucket 留空供使用。文件数据块可能暂留在 JuiceFS trash，沿用 1 天回收设置。
7. 重复执行 bootstrap：所有资源 unchanged，容器正常运行。
8. 旧 RustFS Service、容器及本机 `19000` 监听已移除；仅保留 JuiceFS `19010` 转发。

本次验证证明本地 SSD、多 bucket、分片读写及重启持久性可用。后续用户已取消防覆盖、发布撤权和复制封存要求，客户端自行负责发布后的文件修改；当前契约采用一租户一个 bucket。上面的两个 bucket 仅用于部署测试，不代表真实租户映射。可信 HEAD 校验等剩余要求仍待验证。[原能力验证](../../docs/storage-contract/results-20261015.md)保留历史结果。

官方依据：[JuiceFS 本地磁盘后端](https://juicefs.com/docs/community/getting-started/standalone/)、[多 bucket gateway](https://juicefs.com/docs/community/guide/gateway/#multi-bucket-support)。
