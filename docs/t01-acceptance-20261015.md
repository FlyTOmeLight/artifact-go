# T01 集合服务交付记录

日期：2026-10-15。范围止于T01；T02–T12未开始。T00获批建议已同步本地设计与[父Spec #1](https://github.com/FlyTOmeLight/artifact-go/issues/1)，保留79条用户故事、56项实施决策和20项测试决策的完整编号。

## 已实现

- 创建、列表、读取、归档四个公开集合操作，及对应Go SDK、Controller注解、Swagger/OpenAPI 2.0生成物。
- 明确的可信身份注入、精确租户/项目隔离、历史作用域读取/归档；未提供通过Header自报可信身份的入口。
- 只创建artifacts表。ScopeKey保证NULL项目仍约束重名；scope/type内名称小写规范化后唯一，归档保留命名空间。MySQL表使用大小写敏感排序，保持可信租户/项目ID精确隔离。
- 集合列表按ID升序，游标绑定scope/type/state，默认50条、最多200条；集合返回未配置的默认retentionPolicy，不实现策略修改或执行。
- 公共service构造借用DB，独立BuildWithIdentity由框架拥有连接与退出清理；仅实现当前需要的配置，未生成后续空模块。
- Go 1.27、server-go v0.10.0正式依赖，无replace；可重复的make check、OpenAPI生成和基础GitHub Actions CI。

## 验证

完整检查使用独立Docker PostgreSQL 17及MySQL 8.4，资源由检查脚本创建并在finally清理，不使用现有业务数据库。`make check`执行：

- 四路由真实HTTP/SDK链路、scope隔离、NULL项目/类型命名空间、归档幂等、历史scope与分页游标。
- 8个并发创建同名集合只成功一次；其他请求为409，不以进程内锁代替数据库约束。
- 恶意/错误JSON、null、多文档、未知字段、未认证请求；1 MiB以上合法请求、恰好8 MiB与超出一字节的边界。
- 租户/项目大小写隔离、集合URI租户字符约束、UTC响应时间。
- 两种数据库分别运行独立进程，使用测试专用resolver启动真实app.Run，经SDK创建集合，再SIGTERM并确认正常退出。
- 借用DB不会由Service.Stop关闭；缺resolver拒绝装配；SDK错误、取消、超时、请求身份并发隔离。
- 全部Go测试带race、go vet、入口构建、格式与OpenAPI重新生成一致性。

最终修复后的`make check`全部通过（2026-10-15）：integration 4.782s、SDK 1.766s、unit 1.696s；包含race、vet、build、格式及OpenAPI一致性检查。`go mod verify`通过。CI配置已提供，但未提交/推送，不能声称远端CI已运行。

## 审查后修正

1. 框架v0.10.0的binder固定1 MiB，项目已定8 MiB。Create入口使用标准库有界JSON解码及框架所用validator校验；其他URI/query继续复用binder。超限保持413 RequestBodyTooLarge。
2. 原TenantID允许URI authority分隔字符，可能创建不可解析的artifact URI。已核对现有租户ID生成规则，固定T01的TenantID为1–64个ASCII字母、数字、`_`或`-`，保留大小写；UserID/ProjectID规则未改，ProjectID按路径段编码。设计及Spec同步该边界。
3. 响应时间显式转换为UTC，不向客户端暴露运行机器本地时区。

## 尚未交付的边界

真实用户Bearer及历史scope解析仍需租户接入层实现。`cmd/server`缺resolver会直接失败；测试专用身份映射只存在tests，不作为生产认证。接入使用service.BuildWithIdentity；这一限制沿用获批T00，不把fixture通过写成真实身份联调通过。

版本/草稿/alias/retention执行、storage文件与grants、HTTP引用交接都没有实现，也没有暴露这些路由。所有后续设计数值目前是已确认契约，未伪装成T01的有效配置开关。

仓库已原地git init，main分支、origin指向FlyTOmeLight/artifact-go；现有资料保留，临时目录/凭证/构建物排除。未自动commit、push或发布，也未创建T02及以后的实施任务。

## 对照设计与Spec复审（2026-10-15）

本轮审查当前全部T01未提交文件；仓库尚无HEAD，审查面不是空git diff。对照父Spec、实施计划及两份设计，两个子代理分别核查工程规范和Spec，主审交叉验证并修正。

- 工程规范：修正make check吞掉格式工具/文档生成失败，以及MySQL默认本地时区导致跨时区实例误读DATETIME。通过注入失败命令确认检查入口返回失败；补充UTC/Local/非UTC时区/关闭时间解析的真实连接检查，以及上海子进程写入、独立UTC连接读取同一记录。
- Spec与设计：修正归档事务未显式READ COMMITTED及有界重试，采用最多3次、总预算5秒、间隔10毫秒，仅重试框架确认已回滚的事务错误。新增并发归档与重复归档不更新时间验证。
- 文档一致性：父Spec改用“制定规格时”描述原始无实现基线，明确当前T01状态；本地设计更新实际OpenAPI脚本/输出路径及8 MiB绑定例外。
- 未发现集合四操作、精确scope、编号模式、分页、SDK的其他确定遗漏；后续版本/草稿/storage能力仍未实现，未开始T02。真实身份接入与远端CI仍是已有未验收边界。

复审修正后的`make check`全部通过：integration 4.753s、SDK 1.693s、unit 1.617s；包含真实PostgreSQL/MySQL、跨时区读取、并发归档、进程退出、race、vet、build、格式及OpenAPI一致性。父Spec修改已读回核对。
