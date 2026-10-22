> 最新T00实测见[2026-10-15普通IAM与现行契约报告](t00-juicefs-report.md)，探针脚本为 `scripts/verify_juicefs_t00.py`，运行时须用 `--credentials-file /path/to/credentials.env` 显式提供测试环境凭据，并用 `--admin-helper` 指定辅助程序；`scripts/verify_juicefs_t08.py` 同样要求 `--credentials-file`。下文旧探针与历史报告仍保留；不能把旧摘要/防覆盖失败继续当作当前业务门槛。

# 对象存储直传契约验证

> 最新grill已决定仅核对对象存在和大小，摘要为客户端声明；下列脚本及机器报告仍保留旧严格摘要探针行为，尚未适配本轮决定。本轮只改设计，不能用旧摘要失败阻塞新契约，也不能把旧报告改成通过。

本目录与 `scripts/verify_s3_contract.py` 提供真实端点探针。2026-10-15 的独立 JuiceFS 多 bucket gateway + Redis 环境已退役，相关报告仅保留历史实测证据。当前安装与访问见 [Forge 部署说明](../../deploy/forge/README.md)。RustFS 已按用户要求移除，原报告保留为历史验证记录，不能把 RustFS 的结果用于证明 gateway 通过。当前工具支持 RustFS 原生 STS、JuiceFS gateway STS 请求和 AWS STS，不自动创建 bucket、IAM 用户或更改远端策略。实际入口、镜像版本及结果由部署记录补充。

当前契约采用一租户一个 bucket，同租户各项目共享该 bucket，由服务按可信 TenantID 解析位置。发布后的对象修改由客户端负责；服务不要求条件写、不撤销已签发凭证、不复制封存对象。版本发布冻结数据库文件清单，JSON 快照和固定记录保持不变，固定引用不保证外部对象字节持续不变。确认时对象存在/大小核验、凭证时限、操作范围、跨租户隔离及正常回收要求保留。

## 准备隔离测试范围

在每个实际入口准备两个不同的专用测试 bucket，模拟不同租户；使用 `--bucket` 和 `--other-tenant-bucket` 指定，不把现有本地测试 bucket 当成真实租户配置。认证使用部署提供的长期服务 IAM 用户，权限包括查询 bucket versioning/lifecycle/CORS、对象 HEAD/GET/DELETE、上传及中止分片，且能在第二个测试 bucket 创建和删除探针对象。RustFS STS 临时会话权限是父用户与 session policy 的交集，父用户必须允许上传。可用 `--grant-profile` 将服务查询/清理身份与签发 STS 的上传身份分开。历史 RustFS 部署同时实测 root 派生限权会话与普通 IAM 用户派生会话，不能将结果推广到 JuiceFS 或其他版本。

人工检查并配置 `lifecycle.template.json` 与 `cors.template.json`。替换 bucket 与客户端 origin，合并到已有配置，勿直接覆盖业务配置。旧条件写策略模板已移除；探针上传不附加防覆盖条件头。

测试要求 Versioning 为 Disabled。Enabled 或 Suspended 时，普通 DELETE 可能保留旧版本；工具会明确失败，不把 HEAD 404 当作物理删除。需要这些配置时，应先确定逐版本清理契约和权限再扩展验证。[AWS 版本删除](https://docs.aws.amazon.com/AmazonS3/latest/userguide/DeletingObjectVersions.html)

本工具默认部署参数为凭证 900 秒、会话 3600 秒、单文件 1 GiB、每会话 100 个文件。这些是测试输入，不能当作容量预留或已落实的业务限制。10 GiB 是本地 PVC 的容量请求，JuiceFS 逻辑容量为 9 GiB、缓存为 256 MiB，文件上限还需依据对象存储容量和业务登记限制确定。未完成分片回收默认 1 天，长于会话期限；生命周期只回收未完成分片。[AWS 分片生命周期](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpu-abort-incomplete-mpu-lifecycle-config.html)

## 运行

运行 SDK 时通过 `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` 或具名 profile 提供本次测试身份的凭据；不要复用退役环境的私有凭证文件。root 只能用于 `--service-only` 数据验证；完整 STS 验证需普通 IAM 用户及其限权配置。

安装 `uv`，通过环境变量或具名 profile 提供服务凭证；不要把 secret 写在命令行、报告或仓库文件中。示例中的端点和 bucket 必须换成部署实际值。

```sh
uv run scripts/verify_s3_contract.py \
  --backend juicefs --endpoint http://127.0.0.1:19010 \
  --sts-endpoint http://127.0.0.1:19010 \
  --region us-east-1 --bucket artifact-contract-test \
  --other-tenant-bucket artifact-contract-other-tenant \
  --addressing-style path --origin https://client.example.com \
  --wait-expiry --report /private/tmp/juicefs-contract-report.json
```

RustFS STS 使用根路径 POST，SigV4 的 service 名称为 `s3`；RoleArn 可省略。官方文档规定 900 秒的最短期限；脚本拒绝请求更短期限，记录实际 Expiration，且检查不超出会话截止时间。`--wait-expiry` 实际等待凭证失效后再次请求，不能用本地时间判断代替存储拒绝。[RustFS STS](https://docs.rustfs.com/en/security-compliance/iam/sts)

JuiceFS gateway 改用 `--backend juicefs --endpoint <实际入口>`；其 STS SigV4 service 为 `sts`。root 请求会被拒绝，不能据此推断所有身份都不支持 STS。使用 `--service-only` 可独立检查数据接口，此模式的授权隔离与到期能力始终 pending。

AWS 后端改用 `--backend aws --role-arn <实际上传角色 ARN>`，可省略自定义 endpoint。SDK 固定为 boto3 1.43.113，最低 Python 3.11；无需建立业务 Python 项目。[AWS AssumeRole](https://docs.aws.amazon.com/boto3/latest/reference/services/sts/client/assume_role.html)

每次只操作自动生成的 UUID 子前缀，finally 中止探针创建的分片并删除两个测试 bucket 中的探针 key；发生 cleanup fail 时，只检查报告中的 UUID 范围。退出码 0 为全部通过，1 为失败，2 为仍有 pending。凭证字段和原始异常内容不写入报告；报告记录真实端点、部署参数、校验值、期限及每项状态。依赖下载或无凭证错误发生在客户端初始化时，需要先解决运行环境。

## 自动检查与后续验收

现有旧探针检查包括：单次SHA256、坏校验拒绝、分片组合摘要和可信HEAD（这些摘要项已不属于当前契约，后续需移除），列举/中止分片、其他 key 和其他租户 bucket 写入拒绝、完整对象删除拒绝、读取流提前关闭、删除不存在对象、实际期限与清理。防覆盖检查已取消。先执行合法上传正例，避免把“所有请求都返回 403”误当作权限生效。

跨租户检查先用服务身份在 `--other-tenant-bucket` 写入探针对象以确认目标可用，再验证本租户限权身份写入被拒绝，最后清理该对象。该参数可选；缺少参数或使用 `--service-only` 时，此项必须记录 pending，不能据其他 key 被拒绝推断跨 bucket 隔离已通过。

分片 SHA256 值为各分片 SHA256 的组合校验，包含 `-2` 分片数量后缀；不等于全文件摘要。此探针只批准实测通过的两个 SHA256 组合，不能推导其他算法支持。[AWS 内容校验](https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html)

本轮grill前的数据探针结果见[历史数据报告](juicefs-client-managed-report.json)：不再执行防覆盖检查，单次/分片上传与清理通过，可信校验及 versioning/lifecycle 查询仍失败；service-only 模式下租户隔离和 STS 保持 pending。

历史能力结果见 [2026-10-15 实测报告](results-20261015.md)，该报告按旧契约生成，条件写失败已不属于当前验收阻塞；摘要失败已不属于当前发布门槛；权限与回收等仍需满足的失败项不能略过。以下后续项仍需补充实际证据才能验收任务 02：

- JuiceFS gateway 1.4.1 已独立实测可信校验失败，versioning/lifecycle 查询 NotImplemented。root STS 请求被拒绝，普通 IAM 用户签发、精确 key 与跨租户 bucket 隔离能力尚未验收。基本读写通过不能证明这些要求通过；能力补齐后需对同一固定版本重新验证。
- 浏览器使用实际origin、SDK、代理和外网入口完成单次/分片操作，记录OPTIONS、实际请求及必要响应头；声明摘要不要求后端checksum响应头。配置检查不能证明浏览器路径可用。[S3 CORS](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ManageCorsUsing.html)
- 创建一条专用 UUID 的遗留分片并记录 UploadId、创建时间；在生命周期到期并实际执行后，确认 ListParts 返回 NoSuchUpload。保留前后证据，勿用配置存在代替执行结果。
- 临时身份不能修改 bucket policy：探针只调用只读 GetBucketPolicy，拒绝可作权限证据，但不会冒险调用策略修改 API；需要部署权限审计或隔离环境中的额外验证。
- Go 存储 Adapter 对 context 取消、阻塞读取退出和关闭资源的测试，需要 Adapter 落地后执行；Python 的提前 close 不能替代该结果。



当前接口职责以 [storage 文件设计](../version-service-technical-design-20261013.md) 为准：storage 提供独立文件准备、短期上传/读取授权及回收，artifact 原子绑定文件清单。本文探针中的会话参数属于旧工具输入，尚未按无会话文件契约更新；不得据其结果宣称新接口已通过。
