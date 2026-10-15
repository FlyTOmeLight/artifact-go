# OrbStack 存储部署验证（2026-10-14）

> 历史验证记录：原 RustFS + JuiceFS 环境已按用户要求删除并重建；当前仅运行 JuiceFS + Redis、本地 SSD 后端。见[当前部署记录](verification-20261015.md)。以下记录保留当时版本与环境的实际结果。

## 已部署结果

- Kubernetes context：`orbstack`，单 ARM64 节点；独立 namespace `artifact-storage`。
- Deployment `storage` 单副本，RustFS、Redis、JuiceFS gateway 共处一个 Pod；重启后 `3/3 Ready`。
- PVC `storage-data`：`10Gi`，`local-path`，`ReadWriteOnce`，已 Bound；RustFS 数据、Redis AOF、JuiceFS cache 使用三个独立 subPath。
- 物理 backing 是本机 SSD。`local-path` 的 `10Gi` 是 PVC 请求值，不是磁盘硬配额；JuiceFS 额外设置 9 GiB 文件系统逻辑容量，缓存上限 256 MiB。这不限制 RustFS 原生 bucket 总量。

## 架构和入口

```text
RustFS 原生 S3 :19000 -> RustFS /data (SSD PVC)
JuiceFS S3 :19010 -> JuiceFS gateway --multi-buckets -> Redis 元数据
                                                     -> RustFS juicefs-chunks
```

RustFS 是真正的对象存储服务，JuiceFS 使用其 S3 协议保存数据块；两个入口暴露不同的 bucket 命名空间。`juicefs-chunks` 只保存 JuiceFS 内部数据，不用于 artifact 业务对象。

仅通过 `127.0.0.1` port-forward 暴露，未创建公网 Ingress/NodePort；Redis 只监听同 Pod 的 `127.0.0.1`。

- RustFS endpoint：`http://127.0.0.1:19000`。
- JuiceFS gateway endpoint：`http://127.0.0.1:19010`。
- region：`us-east-1`；SDK 使用 path-style。
- Secret：`artifact-storage/storage-credentials`。
- 本地凭证文件：`/private/tmp/artifact-storage/credentials.env`，权限 `0600`，不进入仓库。`AWS_*` 是 RustFS，`MINIO_ROOT_*` 是 JuiceFS gateway。
- 重建转发：`deploy/storage/port-forward.sh`；Pod 重建后需重新运行。

## 固定版本及实际核验

- RustFS 官方 image：`rustfs/rustfs:1.0.1`，index digest `sha256:1803faef57627e2d9c2e7d89d655d712ddded5389040054987163043fecb6a3c`，manifest 包含 ARM64。
- 实际 `/usr/bin/rustfs --version`：`rustfs 1.0.1`，`linux-aarch64`，Git commit `6de965ae3c965a78ff819fbcd7acd4aa44177d92`。
- JuiceFS：官方 `v1.4.1` Linux ARM64 release tarball，官方 `checksums.txt` SHA256 已核对；镜像 `artifact-juicefs:1.4.1-arm64` 由本机 Docker 构建，与 OrbStack Kubernetes 共享 image store，`imagePullPolicy: Never`。
- 实际 `juicefs --version`：`1.4.1+2026-07-30.0b90c7d`。实际 `gateway --help` 核实 `--multi-buckets`、`--keep-etag`、`--object-meta`。
- Redis `7.4.2-alpine`，AOF 开启，`appendfsync everysec`。

## 实际执行的验证

1. RustFS 创建并读取两个 bucket 的对象：`artifact-second`、`artifact-persistence`。
2. JuiceFS gateway 创建并读取两个 bucket 的对象：`storage-smoke-a`、`storage-smoke-b`。
3. 四个 bucket 的 `persistence/marker` 内容均为 `artifact-storage-persistence-v1`，读回逐字节匹配。
4. RustFS `juicefs-chunks` 中发现 8 个内部对象，包含 `artifact-jfs/chunks/` 数据块，确认写入通过 JuiceFS 到达 RustFS。
5. 执行 `kubectl --context=orbstack -n artifact-storage rollout restart deployment/storage`，整个 Pod 重建；只读重跑以上 marker 和内部数据块检查，全部通过，证明 RustFS 数据和 Redis 元数据持久保存。
6. 验证后数据目录：RustFS `164K`，Redis `44K`，JuiceFS cache `32K`。测试 marker 保留作为重启验证基准。
7. `bash -n` 检查构建和端口转发脚本通过。

## 契约配置与已确认缺口

新建 RustFS `artifact-contract` bucket，仅用于隔离契约探针。

- `AbortIncompleteMultipartUpload` 1 天 lifecycle 配置成功。
- CORS origin `http://localhost:3000`，GET/HEAD/PUT/POST/DELETE、所有请求头、暴露 `ETag` / `x-amz-checksum-sha256` / `x-amz-checksum-type`，配置成功；2026-10-15 补齐两个校验响应头后读回确认完整保存，未改变其他规则，也未重启 Pod。
- 尝试写入禁止缺少 `If-None-Match` 对象创建的 bucket policy 被 RustFS 1.0.1 拒绝：`InvalidArgument: parse policy failed ... invalid key name: 's3:if-none-match'`。因此不能声称 bucket policy 已强制条件写。
- IAM/STS 与完整直传契约由独立探针报告验收；本报告只确认部署、多 bucket 数据通路及重启持久性。

## 复用部署

运行 `deploy/storage/bootstrap.sh`：检查当前 context 为 OrbStack，按需生成权限 `0600` 的随机凭证，创建 Secret，构建 JuiceFS 镜像并部署。已有 Secret 和镜像直接复用，不覆盖凭证。

JuiceFS `format` 会在后端 bucket 不存在时自动创建；已在独立 Redis DB 2 和临时 `juicefs-bootstrap-check` bucket 实测通过，验证后清理该元数据与 bucket。已有部署 `format --no-update` 保留当前文件系统，不使用 `--force`。

官方依据：[JuiceFS gateway](https://juicefs.com/docs/community/guide/gateway/)、[JuiceFS v1.4.1 release](https://github.com/juicedata/juicefs/releases/tag/v1.4.1)、[RustFS 1.0.1 release](https://github.com/rustfs/rustfs/releases/tag/1.0.1)。实际运行行为以本报告记录的版本和探针结果为准。
