# T08 统一 grants 与客户端直传验收

日期：2026-10-15。范围仅 T08；T09 及后续任务未开始，未提交、推送或部署业务服务。

## 实现

新增 `POST /v1/storage/grants` 和 `storagev1.CreateGrant`。请求保留对象结构，包含 1–1000 个唯一 FileID、scope 和可选正整数 ttlSeconds。scope 选择 upload/download 预设或原生 S3 Actions，不能混用；只允许 PutObject、GetObject、ListMultipartUploadParts、AbortMultipartUpload。部署禁止的已知动作返回 403，未知或不支持的动作返回 400，不静默降权。

一批文件必须属于可信身份的同一精确租户/项目作用域及已配置后端。全部通过才返回一组 STS 凭证、连接参数、实际到期时间、排序后的 Actions 和按输入顺序排列的文件位置。策略只含 storage 已分配的精确 key，不接受调用方传入 bucket/key/policy，不扩大到前缀或整桶。超过后端 2048 字节策略限制返回 GrantScopeTooLarge，由客户端拆批。

写类授权只允许未过期 uploading 文件和 active scope；读取允许未过期 ready，以及不受原准备期限限制的 bound 文件，历史 scope 仍需可信身份授权。混合读写动作不会绕过文件状态约束。读取不延长准备期限，不写上传凭证等待时间，也不阻止随后回收。

按 FileID 排序取得全部锁后读取数据库时间，确定一次绝对截止时间：请求/default TTL、部署上限、issuer 到期和适用的文件准备期限取最早值。写授权在事务中先持久预留 LastGrantExpiresAt，再于事务外调用 STS；提交结果不明不签发。签发失败或签后检查失败仍保留预留，供 T07 回收等待。返回写凭证前重新认证身份并锁文件检查状态和期限。

使用官方 AWS STS SDK，禁用自动重试、限制签发超时。当前协议配置针对已验收的 JuiceFS 普通 IAM AssumeRole，不包含 AWS RoleArn/跨账户角色配置。issuer 与 bucket 管理凭据分离；临时 issuer 必须明确真实到期。STS 最短 900 秒，签发预算扣除 5 秒，不向上补足短期限；返回凭证不得超过预留截止时间。凭证响应设置 Cache-Control: no-store，错误不暴露上游凭据。

装配支持独立 storage 和显式 StorageDB 的组合服务。未配置 issuer 不安装 grants 路由；配置样例与默认 1 小时上限已补齐。新增 STS 依赖为 Apache-2.0 的官方模块，纯 Go，沿用现有 AWS SDK；go mod tidy/verify 已执行，没有引入额外运行时。

## 审查与修正

本轮实际启动 3 个子代理，分别负责 grants 业务、STS adapter/真实协议、SDK/公开 HTTP 验收；其中两个独立执行最终 Standards review 和 Spec review。冻结 34 个生产文件审查未发现遗留问题或 T09 越界，主线程复核装配、配置、关键授权与验收证据。

已修正入口身份先于请求解析、凭证响应禁止缓存、最终到期复查、配置切片复制及取消原因传播。验收阶段将 15 分钟实际到期等待与常规 5 分钟测试超时分离；公开 S3 验收使用可 seek 的上传 body。custom 权限反例另行收紧，避免把不存在的 multipart 错误误认成权限拒绝。

## 验证结果

`GOCACHE=/private/tmp/artifact-go-build make check` 第二轮退出码 0：真实 PostgreSQL 17/MySQL 8.4 的集成测试 67.872 秒；SDK、unit、race、vet、两个命令构建、gorgeous 格式和 OpenAPI 重生成一致性均通过。当前共 26 个公开业务 HTTP 操作。第一轮因慢测试等待超过 5 分钟及上传测试 body 不可 seek 失败，修正后重跑通过。

真实普通 IAM 协议快速复验通过：多 key 上传、跨 key/桶拒绝、upload 拒绝读取、download 拒绝写入、custom PutObject 拒绝 ListParts；权限反例明确断言 AccessDenied，另有 multipart 创建、上传、列举、中止和 GET/HEAD/Range 正例。实际到期验证通过，运行 902.33 秒：凭证于 2026-10-15T06:39:15Z 到期，前 10 秒写入成功，到期后 2 秒写入被拒绝，同 key 管理身份写入仍成功。该运行验证实际拒绝及前后正例，没有记录拒绝的具体错误码；新增的到期错误码断言未另行等待一次 15 分钟执行。

公开 HTTP/SDK 到真实 JuiceFS 的 TestStorageGrantLive 已在 PostgreSQL 和 MySQL 分别通过：登记两文件、整批 upload grant、普通 PUT、创建并上传分片、续发凭证后使用原 UploadID 列举及完成、HTTP 确认、download grant、HEAD/Range 读取，以及下载凭证写入拒绝。该链路使用本轮新增公开接口，未用管理凭据替代客户端直传身份。

真实 Chromium 的不同 origin 页面已通过签名 PUT、可读 ETag、GET、HEAD 和 Range 206（内容 ell），验证实际 CORS 链路。浏览器探针使用普通 IAM 的 STS 协议签发凭证；公开 grants API 的装配链路由前述双库 Go SDK 验收覆盖。

受控 HTTP STS/S3 与真实数据库覆盖请求结构、权限与状态隔离、签发失败保留预留、确认/删除竞争、签后身份失效、未知提交不签发、读取与删除排序、配置权限上限和 TTL，以及凭证异常/超长到期/取消等失败路径。bound 读取通过持久状态 fixture 验证；真实 Bind/Release 及带文件版本/草稿交接属于 T09/T10，本轮没有实现这些接口。

本轮临时普通 IAM 用户及策略已删除，私有环境文件和管理 helper 已移除，测试 bucket 数量确认归零，专用数据库容器和端口转发已关闭。验收凭据未写入仓库或报告。远端 CI 与真实生产身份接入未在本轮验证。
