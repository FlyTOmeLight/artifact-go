# T00 JuiceFS 实测（2026-10-15）

本次只运行存储验证探针，没有开始 artifact/storage 业务实现。后端为 OrbStack `artifact-storage` 中 JuiceFS 1.4.1、多 bucket 网关，客户端经 `127.0.0.1:19010` 访问。开始时端口转发已断开，本次恢复了转发；未修改部署参数、PVC 或现有 bucket。

> 本文环境已于 2026-10-22 退役，以下端点与机器报告仅保留当时实测证据。当前安装与访问见 [Forge 部署说明](../../deploy/forge/README.md)。

## 结果与建议

| 验证 | 结果 | 设计影响 |
| --- | --- | --- |
| 普通 IAM 用户签发 STS | 通过；临时用户仅获本次测试前缀的四种 S3 action | issuer 用专用普通 IAM 用户；不能用 root 的 STS 拒绝推断整个后端不支持 |
| 同一 STS 精确授权两个 key | 两个 PUT 均成功 | 多文件 grants 可共用凭证，但必须保留精确 Resource |
| 越 key、越 bucket、DeleteObject、upload 凭证 GET、ListBucket | 全部 AccessDenied | 不需要扩大到 bucket/prefix 来实现上传 |
| multipart create/upload/list/complete/abort | 通过；显式 abort 前可列出 UploadId，之后消失 | upload 预设包含 PutObject、ListMultipartUploadParts、AbortMultipartUpload |
| download GET/HEAD/Range | 通过；Range 返回准确的 `234`；download 凭证 PUT、越 key、越 bucket 均拒绝 | download 预设使用 GetObject |
| 自定义 PutObject-only | PUT 成功，AbortMultipartUpload 拒绝 | 自定义 actions 保留真实 S3 action 名称 |
| DurationSeconds 30/60/899 | 全部 400 `InvalidParameterValue` / `invalid token expiry` | 此后端按至少 900 秒处理；剩余允许有效期不足时拒绝，不能向上取整越过截止 |
| DurationSeconds 900 | 签发成功，真实等待到期后 PUT 拒绝；错误映射见下文 | TTL 上限不能只验证请求参数，还要核对返回 Expiration |
| 1000 个 exact-key Resource 的 session policy | 400；明确 `Session policy shouldn't exceed 2048 characters` | API 的 fileIDs 上限 1000 不等于一次 STS 必能容纳 1000 个文件。超过实际序列化 policy 限制返回 GrantScopeTooLarge；客户端拆批，不改用 wildcard |
| PUT CORS 预检 | `http://localhost:3000` 获允许 origin、method 及签名需要的 headers | 只证明 HTTP CORS 响应；正式浏览器 SDK 流程仍未测试 |
| 实际 GET CORS 响应 | 允许 origin，Expose-Headers 包含 Etag/Content-Range 等 | 只证明响应配置；不宣称已完成浏览器端上传/下载 |
| Pod → Service DNS HTTP | Redis 容器通过 `juicefs.artifact-storage.svc.cluster.local:9000/minio/health/live` 返回 200 | 集群内寻址可达；尚未做容器内签名 SDK 直传 |
| 自动清理遗留 multipart | 未验证 | 显式 Abort 成功不等于一天后自动清理成功；须在 T07/T12 验收 cleanup worker |

主运行 STS Expiration 为 `2026-10-14T23:26:15Z`。真实等待到期后，同一凭证 PUT 返回 `InvalidAccessKeyId`；紧随其后的 root 同 key PUT 成功，随后才清理 IAM。**到期后拒绝已验证**。原探针只接受另外几个错误码，因此机器报告保留原始 `fail`，这不表示后端接受了过期凭证。

当前锁定源码链路为：[IsExpired / IsValid](https://github.com/juicedata/minio/blob/69a6cfc9da65/pkg/auth/credentials.go#L122-L146) 按 Expiration 判断有效性，[GetUser](https://github.com/juicedata/minio/blob/69a6cfc9da65/cmd/iam.go#L1544-L1555) 对失效凭证返回 false，[checkKeyValid](https://github.com/juicedata/minio/blob/69a6cfc9da65/cmd/signature-v4-utils.go#L122-L129) 映射为 InvalidAccessKeyID。实测错误与此实现一致；精确原因由源码链路支持，并非错误码本身唯一表示过期。本次主运行先验证有效凭证再等待，没有临近截止同 STS 的最后一次正例；后续脚本已增加该控制，不冒充本次已经执行。

主运行与所有补测创建的 IAM 用户、policy 和本次对象均已删除；主报告保留删除成功与 HEAD404 证据。端口转发保留供 review 使用，临时构建工具和源文件已清理。

DeleteObject 后 HEAD 404 只证明 S3 不再可见。当前 `--trash-days 1` 会保留回收站内容，不能把它写成 SSD 字节立即回收。保留当前一天配置，容量预算应包含 trash、未完成分片和缓存；本次没有更改该参数。

## 证据

- [主探针机器报告](t00-juicefs-report.json)：普通 IAM、STS、数据路径、到期及最终清理。
- [补测机器报告](t00-juicefs-supplement.json)：短有效期拒绝、2048 字符限制、CORS、显式 multipart 清理及 Pod 网络验证。补测资源已清理。
- [当前版本锁定的 MinIO 依赖](https://github.com/juicedata/juicefs/blob/v1.4.1/go.mod)：替换到 `juicedata/minio` commit `69a6cfc9da65`。
- [STS 实现](https://github.com/juicedata/minio/blob/69a6cfc9da65/cmd/sts-handlers.go)：root 不允许签发 AssumeRole、普通 IAM 路径及 session policy 长度限制，与本次结果一致。源码检查是解释依据，不代替实测。

补测 upload/download 越 key/越 bucket 时，父 IAM 被允许读写两个测试 bucket 内本次随机前缀的实际对象；两个独立 session policy 各只含一个 key。授权 key 读写成功，其他 key/bucket 返回 AccessDenied，因此该补测明确验证了 session policy 收窄，而不仅是父 IAM 拒绝。主运行 upload 越 bucket 的父 IAM 本身也不允许该 bucket，其不足由此补测补齐。

## 历史探针说明

当时探针使用已有 `artifact-data`、`artifact-models` 测试 bucket、本机 19010 端口转发和私有凭据文件；只创建随机前缀对象、临时普通 IAM 用户和 policy，并在 finally 删除本次资源。环境退役后，旧凭据路径和复跑命令已移除；在其他环境运行须重新提供该环境的端点、测试范围和凭据；当前脚本要求用 `--credentials-file /path/to/credentials.env` 显式指定凭据文件，并用 `--admin-helper` 指定辅助程序。

`scripts/juicefs-t00-admin` 是隔离的探针辅助模块，用官方 `madmin-go/v2 v2.2.1` 完成 IAM 创建、policy 绑定和删除；不是业务模块或生产依赖。Python 使用原验证工具相同的 boto3 1.43.113。首次运行可能需要下载工具依赖；完整运行至少等待 900 秒。

脚本保留明确的 HTTP/S3 错误码，但不输出令牌和密钥。过期检查只有 `ExpiredToken` 可直接算作到期原因通过；其他一般拒绝需额外证据，不能仅凭 `AccessDenied` 就宣称到期机制验证通过。

已执行 `ruff check`、`ruff format`、Python 编译检查，辅助模块已独立编译成功。此处静态检查不代替后端实测，也不构成业务实现验收。
