# 关系集合重构规划（Field.Relation → 类级关系集合）

> **状态：已完成**（feature/gql-rework）。实现与本规划的偏差：
> ① 发现并一并根治了第4个bug——file重载重复生成虚拟字段（comments1幽灵），
>    方案为虚拟字段不入文件+构建期再生+同名同关系跳过；
> ② 反向字段命名采用词干拼接 authorComments（非 authoredComments，免动词形态学）；
> ③ 复合自引用外键跳过递归（告警），复合嵌套关系操作编译期拒绝。

## 动机：单指针关系模型的三个已知缺陷

现状 `protocol.Field.Relation *Relation` 是字段上的单指针（loader_base.go 自述
「本期只观测覆盖」），一个列/字段只能承载一条关系。业内方案（postgraphile/hasura）
以约束为一等对象、按 constraint 建关系集合。单指针导致三类静默丢失：

1. **同两表间多条外键，反向列表关系被合并**：`comments.author_id` 与
   `comments.editor_id` 都指向 `users`，去重 key 只按类对
   （metadata.go collectManyToOne / ONE_TO_MANY 分支），`User.comments`
   只建一次，第二条反向入口消失。
2. **复合（多列）外键被拆成多条单列关系**：pg_constraint unnest 成多行
   （loader_pgsql.go），每列各生成一条 MANY_TO_ONE，JOIN 只按其中一列，
   数据错配；还会让中间表判定（`len(fks)==2`）误判。
3. **自引用多对多只剩一个方向**：`user_friends(user_id, friend_id)` 两条
   setRelation 落到同一 field（都是 `users.id`），后者覆盖前者
   （loader_base.go createManyToManyRelation，有覆盖告警）。

## 目标模型

```go
// protocol.Class 增加类级关系集合（约束为一等对象）
type Class struct {
    ...
    Relations []*Relation // 本类参与的全部关系，按约束名/推导键唯一
}

// Relation 升级为支持复合键
type Relation struct {
    Name         string   // 约束名或推导键（去重与覆盖裁决的唯一标识）
    SourceClass  string
    SourceFields []string // 复合键：多列有序对齐
    TargetClass  string
    TargetFields []string
    Type         RelationType
    Through      *Through // 多对多中间表（含双侧键列组）
}
```

- `Field.Relation` 保留为**派生视图**（该字段参与的首条关系），一个大版本内
  兼容现有编译器读取路径；编译器逐步改读 `Class.Relations`。
- GraphQL 字段名由关系名派生：同目标多关系时用源列名去 `_id` 后缀
  （`author`/`editor`），反向为 `authoredComments`/`editedComments`
  （`<源字段>+<复数类名>`，单关系时保持现状 `comments` 不变——兼容优先）。

## 分步实施（每步独立可测可交付）

1. **protocol 扩展**：`Relation` 加 `Name/SourceFields/TargetFields`
   （单数字段保留读兼容），`Class.Relations` 落地；file loader 的序列化
   兼容旧 JSON（单列写法自动升格为单元素数组）。
2. **loader 改造**：pgsql/mysql loader 按 constraint 聚合行（复合键收敛为
   一条 Relation），全部关系进 `Class.Relations`；`Field.Relation` 由集合
   派生。中间表判定改按「约束数==2」而非「外键行数==2」。
3. **metadata 融合与命名**：collectManyToOne/ONE_TO_MANY 去重 key 从
   「类对」改为「关系 Name」，同目标多关系生成多个反向字段（命名规则见上）；
   自引用 M2M 两方向各挂独立关系。
4. **renderer/编译器切换**：关系字段渲染与 JOIN 生成改读关系对象的
   `SourceFields/TargetFields` 列组（relationBond 多列 AND），复合键
   JOIN 正确性用 golden 用例固化。
5. **清理**：删除 `Field.Relation` 写路径与覆盖告警逻辑，派生视图只读。

## 验收基准

- 三个缺陷各一条端到端红→绿测试（同表双外键反向字段、复合外键 JOIN、
  自引用 M2M 双方向）。
- 现有全部 golden 不变（单关系场景字段名与 SQL 零漂移）。
- file 元数据旧格式可加载（升格兼容测试）。

## 风险与边界

- 命名规则变化仅发生在「同目标多关系」场景——该场景现状本就丢字段,
  没有兼容负担；单关系场景严格零变化。
- `metadata.dev.json` 缓存文件版本升级：`Version` 校验不一致时强制重载。
- 工作量估计：protocol/loader 各 ~1d，融合与命名 ~1d，编译器与测试 ~2d。
