# 项目经验

- 为 Artifact/Storage 初始化表结构时，沿用 `service.New`、`service.NewStorage` 的 GORM AutoMigrate；Forge 部署开启 `DB.AutoMigrate` 与 `StorageDB.AutoMigrate`。不要为 GORM 建表另建 migration CLI 或 migrate initContainer，`storage-iam` 初始化保持独立。
- 保留当前 schema 的外键约束。`internal/table/schema.go` 在 AutoMigrate 后显式创建外键，因为 server-go 默认禁用 AutoMigrate 自动建外键；简化部署入口时不能顺带删除这些约束。
- 以上边界来自本轮用户明确要求；文档已按此更新，实现与验证以当前代码和检查结果为准。历史设计、验收记录只说明当时状态，不作为当前契约。
