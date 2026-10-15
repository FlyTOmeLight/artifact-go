# T06 租户 bucket 实施验证

日期：2026-10-15。范围：租户空间创建；不包含文件登记、STS grant、文件回收或 storage 引用交接。

## 创建与归属检查

固定规则为 `t_<24位小写hex>` → `tenant-t-<24位小写hex>`。当前配置一个 BackendID，不保存映射表。管理 HTTP 入口由接入方注入的 TenantServiceVerifier 验证，adapter 使用独立的后端管理凭据。

adapter 先 HeadBucket。明确不存在时调用 CreateBucket；已经存在时检查 ListBuckets 的账户清单。创建成功，以及 BucketAlreadyOwnedByYou / BucketAlreadyExists 竞争结果，都再检查清单和 HeadBucket，才能返回位置。新建返回201，已存在返回200。其他冲突或不可确认的后端失败不能当成功。不可访问返回 StorageBucketUnavailable，清单中没有目标返回 StorageBucketConflict。客户端重试相同 TenantID，名称不变。

ListBuckets 使用 SDK 分页器，最多每页1000项；后端返回 continuation token 时继续，重复 token 时停止并拒绝未找到目标的请求。单次 HTTP 5秒，整个准备操作15秒；不在SDK层自动重试创建。响应丢失后调用方可重试固定名称请求。失败包装保留 errors.Is/As 的原始原因，但外部响应和普通错误日志只显示固定安全消息。

AWS 的 ListBuckets 定义为当前凭据所属账户拥有的 bucket 清单；HeadBucket 成功本身不证明归属，因为公开或跨账户策略也可能允许它。该判断依据 [AWS ListBuckets](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListBuckets.html) 和 [HeadBucket](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadBucket.html)。后端管理凭据需要 CreateBucket、ListBucket（HEAD）及 ListAllMyBuckets 权限，列表权限不下发给终端用户。

## JuiceFS 归属语义及实测

已部署的是 JuiceFS 1.4.1、多 bucket 模式，OrbStack 上原有 `artifact-storage` 环境。探针使用该 volume namespace 的管理身份，密钥从私有文件加载到进程环境，不写入仓库、命令参数或报告。

JuiceFS 一个 volume 的顶层目录就是 bucket；它没有 AWS 式多个独立存储账户。IAM 用户是这个 namespace 中的授权主体，不是独立 bucket owner。其锁定的 MinIO ListBucketsHandler 在没有 ListAllMyBuckets 权限时，会按 ListBucket 权限过滤列表后返回。因此不能把“普通 IAM 用户在 JuiceFS 列表里看得到”描述为独立账户所有权证明。此实现要求为配置的 backend 提供该 namespace 的管理凭据；AWS 则按官方账户清单语义检查。生产若更换兼容后端，须验证相同账户清单语义。

源码依据：[JuiceFS v1.4.1 gateway.go](https://github.com/juicedata/juicefs/blob/v1.4.1/pkg/gateway/gateway.go) 的 MakeBucketWithLocation/ListBuckets；[锁定的 MinIO bucket-handlers.go](https://github.com/juicedata/minio/blob/69a6cfc9da65/cmd/bucket-handlers.go) 的 ListBucketsHandler。当前 JuiceFS 未提供 AWS 新增的 ListBuckets 分页能力；它返回完整可见清单。其底层一次 Readdir 上限10000；本次验证只覆盖开发环境小规模，不声称大于10000桶容量验收。

真实 adapter 探针结果：

| 情况 | 结果 |
| --- | --- |
| 随机合法租户首次创建 | 新建成功，返回固定 bucket |
| 相同租户重试 | 已存在成功，位置相同 |
| 新租户8路并发创建 | 全部成功，1路新建、7路已存在 |
| 对既有 bucket 使用错误密钥 | 拒绝，没有误报成功 |
| 探针资源清理 | 本轮创建的随机空 bucket 删除成功，并通过HEAD确认不存在 |

探针保留为 `tests/integration/tenant_bucket_live_test.go`，仅显式设置 ARTIFACT_TEST_S3_ACCESS_KEY_ID/ARTIFACT_TEST_S3_SECRET_ACCESS_KEY 时执行，默认不触及部署。端点通过 ARTIFACT_TEST_S3_ENDPOINT 显式设置，本次为本地127.0.0.1:19010。执行 `go test -race -v ./tests/integration -run TestTenantBucketLive -count=1`，凭据须通过私有环境注入；不要贴入终端命令或日志。本次不创建 IAM 用户，不操作既有业务 bucket。

跨账户可读但不属于当前账户、冲突无归属证明、分页、取消错误链等通过受控 S3 HTTP 端点验证。它们是协议测试，不能作为真实 AWS 多账户测试结论。

## S3 SDK 选择

采用 AWS 官方 Go v2 S3 `v1.114.3`（发布于2026-10-09），避免自行实现 SigV4、XML错误解析、区域建桶参数及分页。模块要求 Go1.24，项目Go1.27满足；该模块Apache-2.0，纯Go，无新增CGO或外部运行服务。直接使用S3模块，不引入默认AWS配置发现链，管理凭据只来自显式storage配置。

SDK依赖封装在 internal/storage/client，不进入公开 service/SDK 类型。增加AWS核心、Smithy及S3协议内部模块；没有增加数据库映射或通用后端注册表。与当前探针中的旧MinIO SDK相比，官方v2 SDK仍维护且直接提供当前S3分页接口。

实际执行 govulncheck 对 internal/storage/... 的检查：没有可达漏洞，导入包没有已知漏洞；整个项目 require 图中报告1个未被本路径调用的模块漏洞，不把该结果描述为全仓无漏洞。版本、许可证和传递模块根据下载到本机的固定版本源码与 go.mod 核对。
