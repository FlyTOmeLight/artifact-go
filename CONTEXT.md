# 物料版本管理

本领域管理物料的版本快照、引用及保留关系。

## Language

**物料集合（Artifact）**：具有独立类型、归属和编号规则的一组版本，归属一个租户及可选的项目。清理其中全部版本不会消除集合的编号身份。

**物料类型（Artifact Type）**：由调用方在创建集合时指定的稳定业务分类，例如 skill、model、dataset 或 prompt。同一集合的全部版本具有相同类型，类型创建后固定；不同类型拥有独立的集合命名空间，同一类型内名称在租户及作用域内唯一。

**版本快照（Version）**：物料集合中一次成功发布的内容，包括 JSON 内容及可选文件清单。发布后服务保存的 JSON 快照和文件清单不变；外部文件内容由客户端维护，发布后修改由客户端自行负责。

**草稿（Draft）**：物料集合中具有独立身份、尚在编辑的内容；同一集合可以有多份草稿，每份保存 JSON 和文件清单，两者共用 Revision 并可以反复保存，也可以从已发布版本的 JSON 与文件清单开始准备。发布草稿会生成一个独立的版本快照；之后继续编辑或删除草稿不改变已发布内容。草稿尚未获得版本编号，不作为 latest 或 alias 的目标，也不参与版本保留计算。

**别名（Alias）**：在同一个物料集合内指向一个可见版本的命名引用，其目标可以更新。删除后保留修订历史，重新绑定不重置修订号。

**固定版本引用（Fixed Version URI）**：指向一个确定版本的资源引用，固定版本身份及其 JSON 快照和文件清单，不保证外部文件字节不变。保存或使用该引用不形成保留义务，也不保证版本永久可读。

**版本保护（Version Protection）**：由集合管理者统一设置和解除、显式阻止某个版本被删除的保留要求。它不表示某个消费者独立持有的保留要求，也不累计消费者数量。

**集合管理者（Artifact Administrator）**：负责维护指定物料集合的人或系统，负责协调调用方的保留需求，并在解除保护前确认这些需求已结束。本版仅表达业务职责，不对应单独校验的权限或角色。

**存储文件（Storage File）**：具有独立身份的文件资源，包含确认元数据及上传、绑定和回收状态。文件准备不依赖物料集合；同一精确作用域内可由多个草稿和版本共同引用。

**版本文件清单（Version File Manifest）**：版本中逻辑路径到存储文件的引用及元数据快照，随版本发布固定。它描述该版本使用哪些文件，不承担文件上传过程。

**文件绑定（File Binding）**：一个consumer以独立ReferenceID对已就绪文件建立的持久持有关系；storage先独立提交绑定，artifact才能提交业务。引用没有自动到期时间，直到显式释放最后一份引用才回收。

**存储授权（Storage Grant）**：通过统一接口为一批精确文件取得的一份短期STS，scope选择upload/download预设或显式S3 Actions。storage依据可信租户/项目归属、文件状态和期限独立授权；未过期ready及bound可读，不要求业务清单证明。它不是持久引用，也不形成版本保留义务。

**授权范围（Grant Scope）**：一次授权申请中的S3 Actions集合，可选固定预设或直接声明S3原名。它不表示TenantID/ProjectID身份作用域，也不能扩大调用者能访问的文件范围。

**版本保留策略（Retention Policy）**：每个物料集合独立配置的规则，根据版本数量、年龄、显式保护和 alias 引用决定哪些版本继续保留、哪些可自动删除。读取集合的响应包含该集合的策略内容与修订号；预览只计算结果，不执行删除。

**后台任务（Background Task）**：服务内部持久记录的异步工作，分别用于物料版本保留、到期准备操作中止、可靠引用释放与删除收尾，以及存储文件过期与物理回收。通过租约协调多副本，失败按预算退避重试；不作为公开 API 或 SDK 资源。

**逻辑删除（Logical Deletion）**：在artifact本地事务内让版本停止可见、保留墓碑，并持久记录完整ReferenceID释放任务。草稿删除同样记录释放义务；成功响应不保证storage已解除引用。

**物理回收（Physical Reclamation）**：storage 删除已释放或已终止文件的对象，共享文件的释放立即完成；最后引用才等待回收结果完成版本删除。它可以晚于逻辑删除，也可以因存储故障而重试。

**声明摘要（Declared Checksum）**：客户端可选提供的文件内容算法、类型和值；缺失表示未声明。服务只保存声明，不独立验证实际文件摘要，也不将其作为文件的全局身份或去重依据。

**遗留物料（Orphaned Artifact）**：归属租户或项目已删除、但仍保留原归属身份的物料集合。它的已有内容继续遵循自身保留规则，不因归属对象删除而级联清理。

**租户存储空间（Tenant Bucket）**：一个租户独占的文件存储空间，与租户一一对应。同租户的直属物料和各项目共用该空间，其他租户不能访问。创建和维护由租户系统负责触发，文件登记不隐式创建该空间。

**草稿文件清单（Draft File Manifest）**：草稿中可编辑的逻辑路径与FileID映射，以及内部ReferenceID和确认元数据，和JSON一起受Revision控制；保存后文件不因上传准备期限过期而回收。

**存储引用（Storage Reference）**：storage按(ConsumerID,ReferenceID)唯一记录的一次持有关系，固定关联FileID及可信精确scope；业务owner与ReferenceID的映射由artifact清单保存。有效引用存在时保留文件，最后引用释放后安排物理回收。引用released后永不复活，首期墓碑无TTL且不自动清理，即使FileID deleted也保留稳定查询回执；先到的Release写终态墓碑阻止迟到Bind。引用表示保留义务，不是读取权限；释放其中一份引用不撤销同作用域内对仍被保留文件的访问。

**草稿创建请求（Draft Create Request）**：一次创建意图的requestKey、初始请求指纹和原DraftID。用于响应丢失后确认结果；草稿硬删后记录仍在，不会复活草稿。

**引用变更（Reference Change）**：artifact持久保存的一次文件写入请求及其当前attempt：固定请求指纹、完整新增引用计划、原始准备ExpiresAt、执行截止时间、preparing/committed/aborted决策及成功回执。先记录再Bind；成功业务和committed本地原子，失败中止和完整补偿任务本地原子。恢复worker只中止及清理，不代替用户继续提交。

**保存回执（Applied Revision）**：草稿PUT以requestKey确认的原始成功修订号appliedRevision；与expectedRevision的并发覆盖检查分开。成功重试返回该回执与当前完整草稿，当前revision可能更大，客户端不能将旧内容与新revision拼接。

**补偿释放（Compensating Release）**：明确aborted后释放该attempt全部计划新引用，包含发送或返回结果不明的项；每项不早于可信原准备ExpiresAt，不延长原期限，也不给成功引用附加TTL。旧attempt任务使用不可变载荷，不会释放新attempt引用。

## 当前实施边界（2026-10-15）

用户批准[T00全部建议](docs/t00-review-20261015.md)，授权同步设计与父Spec、原地初始化Git，并仅实施T01集合服务。T02–T12未获本轮执行授权，不提前建立版本/草稿/storage业务实现。内部交接采用已确认[HTTP/JSON线协议](docs/storage-internal-http-contract-20261015.md)，服务凭据与用户resolver分离；[开发参数和failed恢复规则](docs/t00-configuration-operations-20261015.md)为确认基线，生产接入仍需真实环境输入。T01现已完成，真实PostgreSQL/MySQL的HTTP/SDK、并发、生命周期、race、vet、build与OpenAPI一致性检查通过；详见[T01交付记录](docs/t01-acceptance-20261015.md)。仓库已初始化，未commit或push。


## T02 实施边界（2026-10-15）

用户已授权实现并复审T02，未授权T03–T12。本轮新增纯JSON版本创建/列表/读取及SDK，当前累计7个业务操作；基于Artifact行锁和持久成功回执分配编号及幂等重放。提交结果不明需只读确认主库，不能盲目重试。实测MySQL原生JSON会丢失超大数精度，Payload/Annotations改用JSON字节（持久[]byte，bytea/longblob），HTTP仍为JSON对象。当前不支持文件/alias/protection/草稿来源组合。结果见[交付记录](docs/t02-acceptance-20261015.md)。

### T03 实施核对（2026-10-15）

本轮已完成alias三操作、注释与保护两操作，以及发布时alias/保护的原子提交，累计12个业务操作。此前T02“不支持aliases/protected”的记录仅描述当时切片，已由本节取代；files与草稿仍未实现。alias共享变更规则由调用方持有Artifact锁和事务；删除保留修订墓碑，latest为动态只读引用。注释和保护共享VersionRevision；expectedRevision显式0走409冲突，缺失/负数400。管理提交结果不明返回503并要求读取当前状态，未引入新的回执表。T04自动淘汰解绑及后续任务未开始。详见T03交付记录。

### T04 实施核对（2026-10-15）

T04已完成保留策略替换/预览、纯JSON版本删除，累计15个业务HTTP操作及对应SDK。发布按拟提交alias进行数量评估，与旧版淘汰同事务；年龄由后台按数据库时间评估。删除清内容、保留身份及去重回执，自动解除alias复用墓碑修订规则。持久BackgroundTask实现领取、续租、过期接管、失败预算及维护命令CAS重排审计；Service拥有worker启动、取消和等待。执行中只持Artifact业务锁，最终加任务锁核验token与租约，避免阻塞续租；提交结果不明不盲目增加失败预算。完整双库、独立进程、真实维护CLI、race/vet/build与OpenAPI检查通过。此前章节的T01/T02/T03状态为历史切片记录，本节为当前实施边界。T05及后续未开始，无storage/文件交接快捷实现。交付细节见docs/t04-acceptance-20261015.md。

### T05 实施核对（2026-10-15）

已完成纯JSON多草稿五操作、从ready版本复制及指定草稿修订发布，累计20个业务HTTP操作及对应SDK。草稿保存用ReferenceChange保存requestKey固定指纹、appliedRevision和空引用计划；创建另保留DraftCreateRequest，草稿硬删不删除两类回执。重放返回当前完整草稿和原保存修订，硬删后返回410身份。CAS冲突须先提交aborted空计划；未知COMMIT仅经主库确认本次AttemptID后才能返回确定冲突。GET按Artifact→Draft锁内读取一致快照，摘要列表不包含内容。纯JSON没有外部绑定，直接在本地事务提交最终决定，不虚构文件准备流程。指定修订发布不改草稿，来源编辑/删除不影响成功回执。完整双库、两独立进程CAS、提交结果不明注入、HTTP/SDK、race/vet/build及OpenAPI检查通过。此前T01–T04段落为历史切片，本节为当前实施边界；T06及后续未开始，非空files仍明确拒绝。详见T05交付记录。
