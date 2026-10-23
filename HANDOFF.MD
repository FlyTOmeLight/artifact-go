# 项目经验

- 为 Artifact/Storage 初始化表结构时，沿用 `service.New`、`service.NewStorage` 的 GORM AutoMigrate；Forge 部署开启 `DB.AutoMigrate` 与 `StorageDB.AutoMigrate`。不要为 GORM 建表另建 migration CLI 或 migrate initContainer，`storage-iam` 初始化保持独立。
- 保留当前 schema 的外键约束。`internal/table/schema.go` 在 AutoMigrate 后显式创建外键，因为 server-go 默认禁用 AutoMigrate 自动建外键；简化部署入口时不能顺带删除这些约束。
- 以上边界来自本轮用户明确要求；文档已按此更新，实现与验证以当前代码和检查结果为准。历史设计、验收记录只说明当时状态，不作为当前契约。
- 通过 Ego Lite 跑本机验收时，不要在 `ego-browser nodejs` 进程里编译或复制 helper：Ego 创建的文件带 `com.apple.quarantine`，启动即被 macOS `SIGKILL`（表现为 `status: null`、无 stderr、无退出码，容易被误判为服务故障）。在普通 shell 预构建到固定路径，或对产物执行 `xattr -d com.apple.quarantine`。验证依据：同一二进制哈希，普通 shell 构建可直接运行，Ego 复制的副本必被 `SIGKILL`，去掉该属性后恢复正常。
