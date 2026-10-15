# T11：持久 URI 与 Resolve 验收

范围：实施计划T11及系统Spec的固定URI/alias定位要求。状态：实现、review与本地验收完成。

## 实现

新增 `POST /v1/resolve` 与 `artifactv1.Client.Resolve`，支持集合、固定版本、普通alias、latest及文件URI。集合URI仅返回artifact；版本或alias同时返回version；文件URI再返回file。alias及latest结果携带实际固定版本身份与文件URI，不返回凭证或文件字节，不建立保留引用。

URI解析与生成集中在reference，逐段编码opaque ProjectID和文件路径，保留大小写；拒绝不支持的scheme/层级、用户信息、端口、query/fragment、非法编码和绝对/点/反斜杠路径。业务先比对可信精确tenant/project，再由主库核对集合type及归属；URI不能切换请求作用域。

Resolve在只读REPEATABLE READ事务中取得集合、保留策略、alias目标、可见版本及文件清单的一致快照。归档集合及可信历史scope可读；已逻辑删除的版本、alias墓碑、缺失文件和跨scope/type返回404。无storage依赖、授权、字节读取或写入副作用。

SDK复用当前身份context和框架错误链，不自动重试。请求严格接受一个含非空uri字符串的JSON对象，沿用流式总请求体上限。OpenAPI扫描加入resolve，业务HTTP操作共27个，内部交接不计入。

## Review

Standards和Spec独立审查均为0项；生产文件与冻结快照一致，无需业务修正。

## 验证

3组公开HTTP集成测试在真实PostgreSQL/MySQL和race下通过（6.116秒），2组URI纯函数测试通过（1.857秒），2组SDK测试通过（1.637秒），相关vet通过。完整make check首次发现1处测试结构体字面量格式，已修正重跑；重跑完整 `GOCACHE=/private/tmp/artifact-go-build make check` 退出码0，真实双库全量race、vet、两个命令构建、格式及OpenAPI重生成一致性均通过。真实身份接入及部署验收属于T12。
