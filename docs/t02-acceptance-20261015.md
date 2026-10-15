# T02 纯 JSON 版本交付记录

日期：2026-10-15。范围为实施计划T02，T03–T12未开始。当前仓库仍无提交，审查覆盖本轮新增及修改文件；设计与Spec描述完整系统，本次只交付其纯JSON版本切片。

## 实现范围

新增POST /v1/artifacts/{artifactID}/versions、GET同集合路径、GET /v1/artifacts/{artifactID}/versions/{versionID}及Go SDK/OpenAPI。累计7个业务HTTP操作。首次发布201、成功重放200；列表只返回ready，按Sequence降序，可按Number精确筛选，cursor绑定scope/ArtifactID/Number。普通读取删除或不存在的版本404。

Artifact行锁串行分配整数或正式三段SemVer编号；版本、集合计数、成功CreateRequest同事务提交。READ COMMITTED，明确已回滚的事务错误最多3次/5秒，失败不占号。幂等键包含ArtifactID、ActorID、RequestKey，同键异参409；归档和归属删除之后仍能确认成功结果，墓碑重放410保留VersionIdentity。成功回执不依赖进程内缓存。

payload/annotations省略默认{}，null/非对象拒绝；数字以json.Number参与指纹，数字词法不同即异参，字段顺序/空白/等价转义归一，数组顺序保留。字段按compact的原始UTF-8字节计量，大小分别默认256 KiB/64 KiB；整包默认8 MiB，实际流式限量。成功重放不受后来调小的字段限影响，仍受整包限影响。公开VersionConfig及Options.Version支持部署配置，显式配置必须三个值均为正。

新建Version/CreateRequest表、owner外键及唯一约束；MySQL使用NO PAD区分请求键尾空格。Payload/Annotations作为校验过的JSON字节保存，SQL类型bytea/longblob，避免数值转换；SDK仍返回JSON对象。

T02拒绝非空files/aliases、protected=true和草稿来源，不静默忽略，也没有新增后续API、storage调用或后台任务。公共DTO保留完整设计字段并标明本次限制。

## 对照 review 与修正

三个子代理分别完成领域、SDK、验收测试；实现后另按工程规范和Spec两条轴复审，主代理交叉验证。

工程规范轴：MySQL8.4原生JSON探针确认18446744073709551617和高精度小数会改变值，改用二进制JSON字节存储；PostgreSQL转义JSON回归进一步要求持久字段为普通[]byte，避免命名RawMessage被驱动按文本处理。未增加依赖。

Spec轴：修复提交结果不明时直接500的问题，改为有界主库只读确认，成功返回原结果/410，确定异参返回409，无法确认返回503 OperationOutcomeUnknown；不盲目再次发布。修复标准库大小写匹配绕过files:null和草稿来源存在检查，入口严格接受契约字段名。补齐集合与发布共用的纯编号规则及单元测试。

## 验证

- 真实PostgreSQL17与MySQL8.4的HTTP/SDK，包括编号、快照、作用域、分页、幂等冲突、不同actor、归档/历史scope重放、默认值/null、数字词法及JSON大小边界。
- 两个独立app.Run进程同时发布：不同键唯一递增，同键只产生一个版本；成功请求在另一个进程重放返回原身份。
- 真实DB上的COMMIT故障注入：SQL和提交/回滚真实执行，只模拟回执错误；已提交确认原结果，未提交503，原键重试不重复占号。
- 复合归属外键拒绝跨集合回执，墓碑仍占编号，整数/SemVer溢出不写成功回执；测试直接准备墓碑，并不代表已实现T04删除API。
- 超uint64整数、高精度小数、1e400、转义JSON持久读取不丢词法；降低字段限仍允许成功重放。
- T01回归、配置/编号纯函数、SDK错误链、race、vet、build、格式及OpenAPI生成一致性。

最终`make check`通过：integration 12.625s、SDK 1.634s、unit 1.634s，包含race；vet、build、格式和OpenAPI重新生成一致性也通过。累计7个业务操作与T01四项+T02三项一致。审查发现的确定问题均已修正。

真实身份系统接入、实际storage与后续文件契约、远端CI仍未验收；当前测试身份不作为生产认证。未commit、push或发布。
