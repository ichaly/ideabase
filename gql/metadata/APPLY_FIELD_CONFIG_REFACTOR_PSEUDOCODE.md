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
    fixedName := ConvertFieldName(fieldConfig.Column, config) // 只调用一次

    // 虚拟字段：直接新建
    if fieldConfig.Column == "" {
        field := createField(class.Name, fieldName, fieldConfig)
        fields[fieldName] = field
        continue
    }

    // 基础字段（表名字段或标准字段名）
    // 优先查找列名，其次标准字段名，都不存在时才新建
    isTableField := fieldName == fieldConfig.Column
    isStandardField := fieldName == fixedName
    if isTableField || isStandardField {
        var field *internal.Field
        if old, ok := class.Fields[fieldConfig.Column]; ok {
            updateField(old, fieldConfig)
            field = old
        } else if old, ok := class.Fields[fixedName]; ok {
            updateField(old, fieldConfig)
            field = old
        } else {
            field = createField(class.Name, fieldName, fieldConfig)
        }
        fields[fieldName] = field
        continue
    }

    // 覆盖模式
    if fieldConfig.Override {
        // 优先查列名字段，其次标准字段名
        baseField, ok := fields[fieldConfig.Column]
        if !ok {
            baseField, ok = fields[fixedName]
        }
        if !ok {
            // 升级为基础字段
            field := createField(class.Name, fieldName, fieldConfig)
            fields[fieldName] = field
            fields[field.Column] = field
        } else {
            // 用新字段名替换原字段名，指针一致
            baseField.Name = fieldName
            updateField(baseField, fieldConfig)
            fields[fieldName] = baseField
            // 只删除标准字段名索引，保留列名索引
            delete(fields, fixedName)
        }
        continue
    }

    // 别名字段
    baseField, ok := fields[fixedName]
    if !ok {
        panic("别名字段必须有基础字段: " + fieldName)
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
