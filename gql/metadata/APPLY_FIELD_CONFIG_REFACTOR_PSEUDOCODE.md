# applyFieldConfig 重构伪代码

本伪代码用于指导元数据字段合成与索引处理的高效重构，实现一次遍历、分组排序、类型分支处理，保证逻辑清晰、效率高、易维护。

```go
// 1. 字段分组与排序（用switch/case简化分组逻辑）
// 这样保证主字段、标准字段优先处理，别名/覆盖/虚拟字段后处理，避免依赖未初始化
var tableFields, classFields, overrideFields, aliasFields, virtualFields []string
for fieldName, fieldConfig := range fieldConfigs {
    switch {
    case fieldConfig.Column == "":
        // 虚拟字段：无物理列，完全自定义
        virtualFields = append(virtualFields, fieldName)
    case fieldConfig.Override:
        // 覆盖字段：用于别名类完全覆盖主类和列字段
        overrideFields = append(overrideFields, fieldName)
    case fieldName == fieldConfig.Column:
        // 表名字段：字段名与列名一致，通常为主字段
        tableFields = append(tableFields, fieldName)
    case fieldName == ConvertFieldName(fieldConfig.Column, config):
        // 标准字段名：字段名为规范化后的列名
        classFields = append(classFields, fieldName)
    default:
        // 其他为别名字段：依赖主字段或标准字段
        aliasFields = append(aliasFields, fieldName)
    }
}
// 拼接顺序：主字段 > 标准字段 > 覆盖 > 别名 > 虚拟
orderedFields := append(tableFields, classFields...)
orderedFields = append(orderedFields, overrideFields...)
orderedFields = append(orderedFields, aliasFields...)
orderedFields = append(orderedFields, virtualFields...)

// 2. 字段合成与赋值（一次遍历，类型分支处理）
fields := make(map[string]*internal.Field)
for _, fieldName := range orderedFields {
    fieldConfig := fieldConfigs[fieldName]
    canonName := ConvertFieldName(fieldConfig.Column, config) // 只调用一次，规范化后的标准名

    // 虚拟字段
    if fieldConfig.Column == "" {
        fields[fieldName] = createField(class.Name, fieldName, fieldConfig)
        continue
    }

    // 主字段、标准字段、覆盖字段统一处理
    // 只用列名做 key，字段 Name 用配置 key（fieldName），合并已有或新建
    if fieldName == fieldConfig.Column || fieldName == canonName || fieldConfig.Override {
        baseField, ok := fields[fieldConfig.Column]
        if ok {
            // 复用已有字段指针，Name 用配置 key
            baseField.Name = fieldName
            updateField(baseField, fieldConfig)
        } else {
            // 没有基础字段，新建，Name 用配置 key
            field := createField(class.Name, fieldName, fieldConfig)
            fields[fieldConfig.Column] = field
        }
        continue
    }

    // 别名/追加字段（必须依赖基础字段）
    baseField, ok := fields[fieldConfig.Column]
    if !ok {
        // 返回错误，由上一级处理（如 Loader 或主流程）
        return fmt.Errorf("别名字段 %s 必须有基础字段 %s", fieldName, fieldConfig.Column)
    }
    field := deepcopy.Copy(baseField).(*internal.Field)
    field.Name = fieldName
    updateField(field, fieldConfig)
    fields[fieldName] = field
}

// 3. 最终整体赋值，保证无历史残留
class.Fields = fields
```

## 说明

- **分组排序**：先分组再排序，保证主字段、标准字段优先，别名/覆盖/虚拟字段后处理，避免依赖未初始化。
- **分组含义**：
  - 主字段（tableFields）：字段名与列名一致，通常为主表字段
  - 标准字段（classFields）：字段名为规范化后的列名
  - 覆盖字段（overrideFields）：用于别名类完全覆盖主类字段
  - 别名字段（aliasFields）：依赖主字段或标准字段
  - 虚拟字段（virtualFields）：无物理列，完全自定义
- **一次遍历**：分支处理所有类型，效率高。
- **索引一致性**：最终整体赋值，保证唯一性和无历史残留。
