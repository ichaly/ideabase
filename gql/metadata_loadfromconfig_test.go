package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadFromConfig 专门测试 loadFromConfig 方法的各种情况
func TestLoadFromConfig(t *testing.T) {
	// 创建测试场景
	t.Run("空配置", func(t *testing.T) {
		v := viper.New()
		meta := &Metadata{
			v:     v,
			cfg:   &internal.Config{},
			Nodes: make(map[string]*internal.Class),
		}

		err := meta.loadFromConfig()
		assert.NoError(t, err, "处理空配置不应该出错")
		assert.Empty(t, meta.Nodes, "空配置不应该添加任何节点")
	})

	t.Run("虚拟类", func(t *testing.T) {
		v := viper.New()
		meta := &Metadata{
			v:     v,
			cfg:   &internal.Config{},
			Nodes: make(map[string]*internal.Class),
		}

		// 设置配置中的虚拟类
		meta.cfg.Metadata.Classes = map[string]*internal.ClassConfig{
			"VirtualClass": {
				// 没有表名，表示虚拟类
				Description: "测试虚拟类",
				PrimaryKeys: []string{"id"},
				Fields: map[string]*internal.FieldConfig{
					"id": {
						Type:      "integer",
						IsPrimary: true,
					},
					"name": {
						Type:        "string",
						Description: "名称字段",
					},
				},
			},
		}

		err := meta.loadFromConfig()
		require.NoError(t, err, "处理虚拟类配置不应该出错")

		// 验证虚拟类被正确创建
		virtualClass, exists := meta.Nodes["VirtualClass"]
		require.True(t, exists, "应该存在VirtualClass")
		assert.True(t, virtualClass.Virtual, "VirtualClass应该是虚拟类")
		assert.Equal(t, "测试虚拟类", virtualClass.Description, "描述应该正确")
		assert.Equal(t, []string{"id"}, virtualClass.PrimaryKeys, "主键应该正确")

		// 验证字段
		assert.Len(t, virtualClass.Fields, 2, "应该有2个字段")
		idField, exists := virtualClass.Fields["id"]
		assert.True(t, exists, "应该存在id字段")
		assert.True(t, idField.IsPrimary, "id应该是主键")
		assert.Equal(t, "integer", idField.Type, "类型应该正确")

		nameField, exists := virtualClass.Fields["name"]
		assert.True(t, exists, "应该存在name字段")
		assert.Equal(t, "string", nameField.Type, "类型应该正确")
		assert.Equal(t, "名称字段", nameField.Description, "描述应该正确")
	})

	t.Run("类别名-不需要字段处理", func(t *testing.T) {
		v := viper.New()
		meta := &Metadata{
			v:     v,
			cfg:   &internal.Config{},
			Nodes: make(map[string]*internal.Class),
		}

		// 先创建一个基类
		baseClass := &internal.Class{
			Name:   "User",
			Table:  "users",
			Fields: make(map[string]*internal.Field),
		}

		// 添加字段
		baseClass.Fields["id"] = &internal.Field{
			Name:      "id",
			Column:    "id",
			Type:      "integer",
			IsPrimary: true,
		}
		baseClass.Fields["name"] = &internal.Field{
			Name:   "name",
			Column: "name",
			Type:   "string",
		}

		// 添加到Nodes
		meta.Nodes["users"] = baseClass

		// 设置配置中的类别名，不改变字段
		meta.cfg.Metadata.Classes = map[string]*internal.ClassConfig{
			"UserAlias": {
				Table:       "users", // 指向已存在的表
				Description: "用户表别名",
			},
		}

		err := meta.loadFromConfig()
		require.NoError(t, err, "处理类别名配置不应该出错")

		// 验证类别名被正确创建
		aliasClass, exists := meta.Nodes["UserAlias"]
		require.True(t, exists, "应该存在UserAlias")
		assert.False(t, aliasClass.Virtual, "UserAlias不应该是虚拟类")
		assert.Equal(t, "用户表别名", aliasClass.Description, "描述应该正确")
		assert.Equal(t, "users", aliasClass.Table, "表名应该正确")

		// 验证字段 - 应该直接复用基类字段的指针
		assert.Len(t, aliasClass.Fields, 2, "应该有2个字段")
		assert.Same(t, baseClass.Fields["id"], aliasClass.Fields["id"], "应该复用原字段对象")
		assert.Same(t, baseClass.Fields["name"], aliasClass.Fields["name"], "应该复用原字段对象")

		// 验证通过表名索引能找到类别名
		tableClass, exists := meta.Nodes["users"]
		assert.True(t, exists, "应该能通过表名找到类")
		assert.Same(t, aliasClass, tableClass, "表名应该指向类别名类")
	})

	t.Run("类别名-需要字段处理", func(t *testing.T) {
		v := viper.New()
		meta := &Metadata{
			v:     v,
			cfg:   &internal.Config{},
			Nodes: make(map[string]*internal.Class),
		}

		// 先创建一个基类
		baseClass := &internal.Class{
			Name:   "User",
			Table:  "users",
			Fields: make(map[string]*internal.Field),
		}

		// 添加字段
		baseClass.Fields["id"] = &internal.Field{
			Name:      "id",
			Column:    "id",
			Type:      "integer",
			IsPrimary: true,
		}
		baseClass.Fields["name"] = &internal.Field{
			Name:   "name",
			Column: "name",
			Type:   "string",
		}
		baseClass.Fields["email"] = &internal.Field{
			Name:   "email",
			Column: "email",
			Type:   "string",
		}

		// 添加到Nodes
		meta.Nodes["users"] = baseClass

		// 设置配置中的类别名，修改字段
		meta.cfg.Metadata.Classes = map[string]*internal.ClassConfig{
			"UserProfile": {
				Table:         "users", // 指向已存在的表
				Description:   "用户资料表",
				ExcludeFields: []string{"email"}, // 排除email字段
				Fields: map[string]*internal.FieldConfig{
					"name": {
						Description: "用户名称", // 修改描述
					},
					"avatar": { // 添加新字段
						Type:        "string",
						Description: "头像URL",
					},
				},
			},
		}

		err := meta.loadFromConfig()
		require.NoError(t, err, "处理类别名配置不应该出错")

		// 验证类别名被正确创建
		aliasClass, exists := meta.Nodes["UserProfile"]
		require.True(t, exists, "应该存在UserProfile")
		assert.False(t, aliasClass.Virtual, "UserProfile不应该是虚拟类")
		assert.Equal(t, "用户资料表", aliasClass.Description, "描述应该正确")
		assert.Equal(t, "users", aliasClass.Table, "表名应该正确")

		// 验证字段 - 应该深度复制后修改
		assert.NotContains(t, aliasClass.Fields, "email", "email字段应该被排除")
		assert.Contains(t, aliasClass.Fields, "id", "id字段应该存在")
		assert.Contains(t, aliasClass.Fields, "name", "name字段应该存在")
		assert.Contains(t, aliasClass.Fields, "avatar", "avatar字段应该被添加")

		// 验证字段属性
		nameField := aliasClass.Fields["name"]
		assert.Equal(t, "用户名称", nameField.Description, "字段描述应该被更新")
		assert.NotSame(t, baseClass.Fields["name"], nameField, "字段应该被深度复制，不是同一个对象")

		avatarField := aliasClass.Fields["avatar"]
		assert.Equal(t, "string", avatarField.Type, "字段类型应该正确")
		assert.Equal(t, "头像URL", avatarField.Description, "字段描述应该正确")
	})

	t.Run("更新现有类", func(t *testing.T) {
		v := viper.New()
		meta := &Metadata{
			v:     v,
			cfg:   &internal.Config{},
			Nodes: make(map[string]*internal.Class),
		}

		// 先创建一个类
		existingClass := &internal.Class{
			Name:        "Product",
			Table:       "products",
			Fields:      make(map[string]*internal.Field),
			Description: "旧产品描述",
		}

		// 添加字段
		existingClass.Fields["id"] = &internal.Field{
			Name:      "id",
			Column:    "id",
			Type:      "integer",
			IsPrimary: true,
		}
		existingClass.Fields["name"] = &internal.Field{
			Name:        "name",
			Column:      "name",
			Type:        "string",
			Description: "旧名称描述",
		}
		existingClass.Fields["price"] = &internal.Field{
			Name:   "price",
			Column: "price",
			Type:   "decimal",
		}

		// 添加到Nodes - 确保添加到正确的键
		meta.Nodes["Product"] = existingClass  // 使用类名作为键
		meta.Nodes["products"] = existingClass // 使用表名作为键

		// 设置配置中的更新
		meta.cfg.Metadata.Classes = map[string]*internal.ClassConfig{
			"Product": {
				Table:         "products",        // 确保设置表名，这很重要
				Description:   "新产品描述",           // 更新描述
				ExcludeFields: []string{"price"}, // 排除价格字段
				Fields: map[string]*internal.FieldConfig{
					"name": {
						Description: "新名称描述", // 更新字段描述
					},
					"stock": { // 添加新字段
						Type:        "integer",
						Description: "库存数量",
					},
				},
			},
		}

		err := meta.loadFromConfig()
		require.NoError(t, err, "更新现有类不应该出错")

		// 验证类被正确更新
		updatedClass, exists := meta.Nodes["Product"]
		require.True(t, exists, "应该存在Product类")
		assert.Equal(t, "新产品描述", updatedClass.Description, "类描述应该被更新")
		assert.Same(t, existingClass, updatedClass, "应该是同一个类对象")

		// 验证字段更新
		assert.NotContains(t, updatedClass.Fields, "price", "price字段应该被排除")
		assert.Contains(t, updatedClass.Fields, "id", "id字段应该保留")
		assert.Contains(t, updatedClass.Fields, "name", "name字段应该保留")
		assert.Contains(t, updatedClass.Fields, "stock", "stock字段应该被添加")

		// 验证字段属性
		nameField := updatedClass.Fields["name"]
		assert.Equal(t, "新名称描述", nameField.Description, "字段描述应该被更新")

		stockField := updatedClass.Fields["stock"]
		assert.Equal(t, "integer", stockField.Type, "字段类型应该正确")
		assert.Equal(t, "库存数量", stockField.Description, "字段描述应该正确")
	})

	t.Run("复杂关系处理", func(t *testing.T) {
		v := viper.New()
		meta := &Metadata{
			v:     v,
			cfg:   &internal.Config{},
			Nodes: make(map[string]*internal.Class),
		}

		// 设置配置中的关系类
		meta.cfg.Metadata.Classes = map[string]*internal.ClassConfig{
			"Department": {
				Description: "部门",
				Fields: map[string]*internal.FieldConfig{
					"id": {
						Type:      "integer",
						IsPrimary: true,
					},
					"name": {
						Type: "string",
					},
				},
			},
			"Employee": {
				Description: "员工",
				Fields: map[string]*internal.FieldConfig{
					"id": {
						Type:      "integer",
						IsPrimary: true,
					},
					"name": {
						Type: "string",
					},
					"deptId": {
						Type: "integer",
						Relation: &internal.RelationConfig{
							TargetClass: "Department",
							TargetField: "id",
							Type:        "many_to_one",
						},
					},
				},
			},
		}

		err := meta.loadFromConfig()
		require.NoError(t, err, "处理关系配置不应该出错")

		// 验证关系处理
		employee, exists := meta.Nodes["Employee"]
		require.True(t, exists, "应该存在Employee类")

		deptIdField, exists := employee.Fields["deptId"]
		require.True(t, exists, "应该存在deptId字段")
		require.NotNil(t, deptIdField.Relation, "deptId应该有关系定义")
		assert.Equal(t, "Department", deptIdField.Relation.TargetClass, "关系目标类应该是Department")
		assert.Equal(t, "id", deptIdField.Relation.TargetField, "关系目标字段应该是id")
		assert.Equal(t, internal.MANY_TO_ONE, deptIdField.Relation.Type, "关系类型应该是many_to_one")
	})

	t.Run("综合场景", func(t *testing.T) {
		v := viper.New()
		meta := &Metadata{
			v:     v,
			cfg:   &internal.Config{},
			Nodes: make(map[string]*internal.Class),
		}

		// 创建基础数据
		// 1. Category表
		categoryClass := &internal.Class{
			Name:   "Category",
			Table:  "categories",
			Fields: make(map[string]*internal.Field),
		}
		categoryClass.Fields["id"] = &internal.Field{
			Name:      "id",
			Column:    "id",
			Type:      "integer",
			IsPrimary: true,
		}
		categoryClass.Fields["name"] = &internal.Field{
			Name:   "name",
			Column: "name",
			Type:   "string",
		}
		meta.Nodes["Category"] = categoryClass   // 使用类名作为键
		meta.Nodes["categories"] = categoryClass // 使用表名作为键

		// 2. Item表
		itemClass := &internal.Class{
			Name:   "Item",
			Table:  "items",
			Fields: make(map[string]*internal.Field),
		}
		itemClass.Fields["id"] = &internal.Field{
			Name:      "id",
			Column:    "id",
			Type:      "integer",
			IsPrimary: true,
		}
		itemClass.Fields["name"] = &internal.Field{
			Name:   "name",
			Column: "name",
			Type:   "string",
		}
		itemClass.Fields["price"] = &internal.Field{
			Name:   "price",
			Column: "price",
			Type:   "decimal",
		}
		itemClass.Fields["categoryId"] = &internal.Field{
			Name:   "categoryId",
			Column: "category_id",
			Type:   "integer",
			Relation: &internal.Relation{
				SourceClass: "Item",
				SourceField: "categoryId",
				TargetClass: "Category",
				TargetField: "id",
				Type:        internal.MANY_TO_ONE,
			},
		}
		meta.Nodes["Item"] = itemClass  // 使用类名作为键
		meta.Nodes["items"] = itemClass // 使用表名作为键

		// 设置多种类型的配置
		meta.cfg.Metadata.Classes = map[string]*internal.ClassConfig{
			// 1. 虚拟类
			"Statistic": {
				Description: "统计数据",
				Fields: map[string]*internal.FieldConfig{
					"totalSales": {
						Type:        "decimal",
						Description: "总销售额",
					},
					"itemCount": {
						Type:        "integer",
						Description: "商品数量",
					},
				},
			},

			// 2. 类别名（不需要字段处理）
			"Product": {
				Table:       "items", // 指向Item表
				Description: "产品信息",
			},

			// 3. 类别名（需要字段处理）
			"ProductExtended": {
				Table:         "items", // 指向Item表
				Description:   "扩展产品信息",
				ExcludeFields: []string{"price"}, // 排除price字段
				Fields: map[string]*internal.FieldConfig{
					"description": {
						Type:        "string",
						Description: "产品描述",
					},
				},
			},

			// 4. 更新现有类
			"Category": {
				Table:       "categories", // 确保设置表名
				Description: "产品分类",       // 更新描述
				Fields: map[string]*internal.FieldConfig{
					"code": { // 添加新字段
						Type:        "string",
						Description: "分类代码",
					},
				},
			},

			// 5. 复杂关系类
			"Tag": {
				Description: "标签",
				Fields: map[string]*internal.FieldConfig{
					"id": {
						Type:      "integer",
						IsPrimary: true,
					},
					"name": {
						Type: "string",
					},
				},
			},

			"ItemTag": {
				Description: "商品标签关联",
				Fields: map[string]*internal.FieldConfig{
					"itemId": {
						Type: "integer",
						Relation: &internal.RelationConfig{
							TargetClass: "Item",
							TargetField: "id",
							Type:        "many_to_one",
						},
					},
					"tagId": {
						Type: "integer",
						Relation: &internal.RelationConfig{
							TargetClass: "Tag",
							TargetField: "id",
							Type:        "many_to_one",
						},
					},
				},
			},
		}

		err := meta.loadFromConfig()
		require.NoError(t, err, "处理综合场景配置不应该出错")

		// 验证1: 虚拟类
		statistic, exists := meta.Nodes["Statistic"]
		require.True(t, exists, "应该存在Statistic虚拟类")
		assert.True(t, statistic.Virtual, "Statistic应该是虚拟类")
		assert.Equal(t, "统计数据", statistic.Description, "描述应该正确")
		assert.Len(t, statistic.Fields, 2, "应该有2个字段")

		// 验证2: 类别名（不需要字段处理）
		product, exists := meta.Nodes["Product"]
		require.True(t, exists, "应该存在Product类别名")
		assert.False(t, product.Virtual, "Product不应该是虚拟类")
		assert.Equal(t, "产品信息", product.Description, "描述应该正确")
		assert.Equal(t, "items", product.Table, "表名应该正确")
		assert.Len(t, product.Fields, 4, "应该有4个字段")
		assert.Same(t, itemClass.Fields["id"], product.Fields["id"], "应该复用原字段对象")

		// 验证3: 类别名（需要字段处理）
		productExt, exists := meta.Nodes["ProductExtended"]
		require.True(t, exists, "应该存在ProductExtended类别名")
		assert.False(t, productExt.Virtual, "ProductExtended不应该是虚拟类")
		assert.Equal(t, "扩展产品信息", productExt.Description, "描述应该正确")
		assert.NotContains(t, productExt.Fields, "price", "price字段应该被排除")
		assert.Contains(t, productExt.Fields, "description", "description字段应该被添加")
		assert.NotSame(t, itemClass.Fields["id"], productExt.Fields["id"], "应该深度复制字段")

		// 验证4: 更新现有类
		category, exists := meta.Nodes["Category"]
		require.True(t, exists, "应该存在Category类")
		assert.Equal(t, "产品分类", category.Description, "描述应该被更新")
		assert.Contains(t, category.Fields, "code", "code字段应该被添加")
		assert.Same(t, categoryClass, category, "应该是同一个类对象")

		// 验证5: 复杂关系类
		itemTag, exists := meta.Nodes["ItemTag"]
		require.True(t, exists, "应该存在ItemTag关系类")
		assert.True(t, itemTag.Virtual, "ItemTag应该是虚拟类")

		// 验证关系字段
		itemIdField, exists := itemTag.Fields["itemId"]
		require.True(t, exists, "应该存在itemId字段")
		require.NotNil(t, itemIdField.Relation, "itemId应该有关系定义")
		assert.Equal(t, "Item", itemIdField.Relation.TargetClass, "关系目标类应该是Item")

		tagIdField, exists := itemTag.Fields["tagId"]
		require.True(t, exists, "应该存在tagId字段")
		require.NotNil(t, tagIdField.Relation, "tagId应该有关系定义")
		assert.Equal(t, "Tag", tagIdField.Relation.TargetClass, "关系目标类应该是Tag")

		// 验证表名索引
		itemsTable, exists := meta.Nodes["items"]
		assert.True(t, exists, "应该存在items表索引")
		assert.Same(t, productExt, itemsTable, "items表应该指向最后创建的ProductExtended类")
	})
}
