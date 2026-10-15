# T06 租户存储空间验收

日期：2026-10-15。范围仅 T06；T07–T12 未开始，未提交、推送或部署业务服务。

## 实现

新增 `POST /v1/storage/tenant-buckets` 及独立 `storagev1.CreateTenantBucket`。请求仅包含 tenantID，严格按 `t_<24小写hex>` 派生 `tenant-t-<24hex>`。响应只含 tenantID、backendID、bucket；首次创建201，已存在且确认归属和访问成功返回200。没有新表、创建回执或bucket删除接口。

管理路由独立使用接入方注入的 TenantServiceVerifier，不经过普通用户 IdentityResolver，也不使用用户Header授权。Storage配置为空时不安装该路由；配置了Storage却缺少管理验证器时构造和装配提前失败。组合入口使用 Options，独立装配使用 BuildWithVerifiers。真实租户系统鉴权仍需接入方落实。

S3 adapter 使用 AWS SDK for Go v2，通过 HEAD 与 ListBuckets核对访问和账号所属清单；创建冲突再次核验，不能把任意409当作成功。请求取消保留原始错误链，对外仅返回安全错误；单次S3请求5秒、整体操作15秒，不自动重试。API失败不等于bucket必然未创建，调用方可按同一tenantID再次确认。

JuiceFS的一个volume为同一存储命名空间，IAM用户不构成独立AWS账号。配置须使用该命名空间管理凭据；实际探针验证当前管理凭据下的操作，不声称验证了JuiceFS中不存在的独立账号所有权模型。依赖选择、实测及清理见[存储探针记录](t06-storage-probe-20261015.md)。

## Standards review

独立审查未发现需修正的生产实现问题。收尾将临时包内存储探针迁至集中tests目录，保留环境变量门控。公开配置不暴露internal或第三方类型，SDK凭证逐请求传递，无共享Header可变状态。

## Spec review

对照系统Spec #1条款33/34/40及设计的租户空间管理规则，最终未发现缺漏或越界。预审发现S3取消错误被替换为503后丢失cause，已修复为保留错误链并返回脱敏错误；补充取消测试及HTTP错误码断言。

## 验证

完整检查命令：`GOCACHE=/private/tmp/artifact-go-build make check`。最终检查通过，退出码0；包含整理后的全部测试。

公开HTTP覆盖真实PostgreSQL 17/MySQL 8.4服务装配，配合受控S3验证新建、重复、8路并发、非法输入、管理/用户身份隔离、不可访问、其他账号、冲突未确认、缺少验证器与禁用入口。SDK覆盖传输、并发凭证隔离、错误与取消；adapter测试覆盖所属清单分页及取消错误链；配置测试覆盖非法端点、缺少必要字段及提前失败。

实际JuiceFS另行运行环境门控探针，验证新建、重复、8路并发及错误凭证拒绝，并清理专用随机空桶。全量检查包括既有T01–T05回归、race、vet、build、格式和生成物一致性；OpenAPI共21个业务操作。

本轮实际启动3个子代理，负责存储adapter/真实探针、SDK及Standards审查、HTTP测试及Spec审查；主线程完成装配、配置、文档、生成物与集成验证。真实生产鉴权、远端CI以及T07之后能力仍不属于本次已验收范围。
