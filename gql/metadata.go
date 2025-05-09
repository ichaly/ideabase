package gql

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/metadata"
	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std"
	"github.com/jinzhu/inflection"
	"gorm.io/gorm"
)

func init() {
	inflection.AddUncountable("children")
	strcase.ConfigureAcronym("ID", "Id")
}

// Metadata 表示GraphQL元数据
type Metadata struct {
	k   *std.Konfig
	db  *gorm.DB
	cfg *internal.Config

	// 统一索引: 支持类名、表名、原始表名查找
	Nodes   map[string]*internal.Class `json:"nodes"`
	Version string                     `json:"version"`
}

// MetadataOption 用于自定义Loader注册与移除
type MetadataOption func(*metadataOptions)

type metadataOptions struct {
	loaders []metadata.Loader
}

// WithLoader 添加或替换Loader
func WithLoader(loader metadata.Loader) MetadataOption {
	return func(opts *metadataOptions) {
		if loader == nil {
			return
		}
		// 替换同名Loader
		for i, l := range opts.loaders {
			if l.Name() == loader.Name() {
				opts.loaders[i] = loader
				return
			}
		}
		opts.loaders = append(opts.loaders, loader)
	}
}

// WithoutLoader 移除指定名称的Loader
func WithoutLoader(names ...string) MetadataOption {
	return func(opts *metadataOptions) {
		for _, name := range names {
			for i := 0; i < len(opts.loaders); {
				if opts.loaders[i].Name() == name {
					opts.loaders = append(opts.loaders[:i], opts.loaders[i+1:]...)
				} else {
					i++
				}
			}
		}
	}
}

// HookedLoader 装饰器，支持afterLoad钩子
// 用于Loader加载后自动执行额外操作（如保存文件）
type HookedLoader struct {
	metadata.Loader
	afterLoad func(h metadata.Hoster) error
}

func (my *HookedLoader) Load(h metadata.Hoster) error {
	err := my.Loader.Load(h)
	if err != nil {
		return err
	}
	if my.afterLoad != nil {
		return my.afterLoad(h)
	}
	return nil
}

// NewMetadata 策略模式重构，支持Loader注册与优先级排序
func NewMetadata(k *std.Konfig, d *gorm.DB, opts ...MetadataOption) (*Metadata, error) {
	cfg := &internal.Config{Schema: internal.SchemaConfig{TypeMapping: dataTypes}}
	k.SetDefault("schema.schema", "public")
	k.SetDefault("schema.default-limit", 10)
	k.SetDefault("schema.enable-singular", true)
	k.SetDefault("schema.enable-camel-case", true)
	k.SetDefault("schema.table-prefix", []string{})

	if err := k.Unmarshal(cfg); err != nil {
		return nil, err
	}

	my := &Metadata{
		k:       k,
		db:      d,
		cfg:     cfg,
		Nodes:   make(map[string]*internal.Class),
		Version: time.Now().Format("20060102150405"),
	}

	// 默认Loader注册，Pgsql和Mysql用HookedLoader包装，dev模式下自动保存
	afterLoad := func(h metadata.Hoster) error {
		if cfg.Mode == "dev" {
			path := filepath.Join(cfg.Root, "cfg", "metadata.json")
			if meta, ok := h.(*Metadata); ok {
				return meta.saveToFile(path)
			}
		}
		return nil
	}
	defaultLoaders := []metadata.Loader{
		&HookedLoader{metadata.NewPgsqlLoader(cfg, d), afterLoad},
		&HookedLoader{metadata.NewMysqlLoader(cfg, d), afterLoad},
		metadata.NewFileLoader(cfg),
		metadata.NewConfigLoader(cfg),
	}
	options := &metadataOptions{loaders: defaultLoaders}
	// 应用自定义选项
	for _, opt := range opts {
		opt(options)
	}
	// 按优先级排序
	loaders := options.loaders
	if len(loaders) > 1 {
		// 降序，优先级高的后加载
		for i := 0; i < len(loaders)-1; i++ {
			for j := i + 1; j < len(loaders); j++ {
				if loaders[i].Priority() > loaders[j].Priority() {
					loaders[i], loaders[j] = loaders[j], loaders[i]
				}
			}
		}
	}
	// 依次执行Loader
	for _, loader := range loaders {
		if loader.Support(cfg, d) {
			if err := loader.Load(my); err != nil {
				log.Warn().Err(err).Str("loader", loader.Name()).Msg("加载器执行失败")
			}
		}
	}
	// 统一关系处理
	my.processRelations()
	return my, nil
}

// Metadata 实现Hoster接口
func (my *Metadata) PutNode(node *internal.Class) error {
	if node == nil || node.Name == "" {
		return nil
	}

	// 1. 处理驼峰命名
	if my.cfg != nil && my.cfg.Schema.EnableCamelCase {
		tableName := node.Table

		// 处理表名前缀
		for _, prefix := range my.cfg.Schema.TablePrefix {
			if prefix != "" && strings.HasPrefix(tableName, prefix) {
				tableName = strings.TrimPrefix(tableName, prefix)
				break // 一旦找到匹配的前缀就停止,否则可能会导致多次处理
			}
		}

		// 处理类名大驼峰（复数转单数）
		if node.Name == node.Table {
			node.Name = strcase.ToCamel(inflection.Singular(tableName))
		}

		// 处理字段小驼峰
		fields := make(map[string]*internal.Field, len(node.Fields))
		for _, field := range node.Fields {
			if field.Name == field.Column {
				field.Name = strcase.ToLowerCamel(field.Column)
			}
			fields[field.Column] = field
			if field.Name != field.Column {
				fields[field.Name] = field
			}
		}
		node.Fields = fields
	}

	// 2. 处理索引
	my.Nodes[node.Table] = node
	if node.Table != node.Name {
		my.Nodes[node.Name] = node
	}

	return nil
}

func (my *Metadata) GetNode(name string) (*internal.Class, bool) {
	n, ok := my.Nodes[name]
	return n, ok
}

func (my *Metadata) SetVersion(version string) {
	my.Version = version
}

// FindClass 根据类名查找类
func (my *Metadata) FindClass(className string, virtual bool) (*internal.Class, bool) {
	if node, ok := my.Nodes[className]; ok && node.Virtual == virtual {
		return node, true
	}
	return nil, false
}

// FindField 根据类名和字段名查找字段
func (my *Metadata) FindField(className, fieldName string, virtual bool) (*internal.Field, bool) {
	if node, ok := my.Nodes[className]; ok && node.Virtual == virtual {
		if field := node.Fields[fieldName]; field != nil && field.Virtual == virtual {
			return field, true
		}
	}
	return nil, false
}

// FindRelation 获取外键关系(支持字段名或列名)
func (my *Metadata) FindRelation(sourceTable, nameOrColumn string) (*internal.Relation, bool) {
	if node, ok := my.Nodes[sourceTable]; ok {
		if field := node.Fields[nameOrColumn]; field != nil {
			return field.Relation, field.Relation != nil
		}
	}
	return nil, false
}

// TableName 获取类的表名
func (my *Metadata) TableName(className string, virtual bool) (string, bool) {
	if node, ok := my.Nodes[className]; ok && node.Virtual == virtual {
		return node.Table, len(node.Table) > 0
	}
	return "", false
}

// ColumnName 获取字段的列名
func (my *Metadata) ColumnName(className, fieldName string, virtual bool) (string, bool) {
	if node, ok := my.Nodes[className]; ok && node.Virtual == virtual {
		if field := node.Fields[fieldName]; field != nil && field.Virtual == virtual {
			return field.Column, len(field.Column) > 0
		}
	}
	return "", false
}

// MarshalJSON 自定义JSON序列化
func (my *Metadata) MarshalJSON() ([]byte, error) {
	// 仅导出key和类名相同的节点
	nodes := make(map[string]*internal.Class)
	for key, class := range my.Nodes {
		if key == class.Name {
			// 直接使用原始对象，减少字段复制
			nodes[key] = class
		}
	}
	return json.Marshal(Metadata{
		Nodes:   nodes,
		Version: my.Version,
	})
}

// processRelations 处理关系图并创建关系字段
func (my *Metadata) processRelations() {
	log.Debug().Msg("处理所有关系信息")

	// 定义关系字段信息结构体
	type RelationFieldInfo struct {
		SourceClass  string
		TargetClass  string
		FieldName    string
		IsReverse    bool
		IsList       bool
		Nullable     bool
		Description  string
		IsThrough    bool
		RelationType internal.RelationType
	}

	// 存储所有需要创建的关系字段
	fieldsToCreate := make([]RelationFieldInfo, 0)
	// 用于避免重复创建反向关系字段的映射
	reverseRelationKeys := make(map[string]bool)

	// 添加关系字段信息的辅助函数
	addRelationField := func(sourceClass, targetClass string, isList, nullable, isReverse, isThrough bool,
		relType internal.RelationType, fieldName string, description string) {

		fieldsToCreate = append(fieldsToCreate, RelationFieldInfo{
			SourceClass:  sourceClass,
			TargetClass:  targetClass,
			FieldName:    fieldName,
			IsReverse:    isReverse,
			IsList:       isList,
			Nullable:     nullable,
			Description:  description,
			IsThrough:    isThrough,
			RelationType: relType,
		})
	}

	// 创建描述文本的辅助函数
	createDescription := func(targetClass string, isList bool) string {
		if isList {
			return "关联的" + targetClass + "列表"
		}
		return "关联的" + targetClass
	}

	// 第一阶段：收集所有关系字段信息
	for className, class := range my.Nodes {
		// 跳过表名索引，只处理类名索引
		if className != class.Name {
			continue
		}

		for fieldName, field := range class.Fields {
			// 跳过非主字段或没有关系的字段
			if fieldName != field.Name || field.Relation == nil {
				continue
			}

			// 获取并补充关系信息
			relation := field.Relation
			if relation.SourceClass == "" {
				relation.SourceClass = class.Name
			}
			if relation.SourceField == "" {
				relation.SourceField = field.Name
			}

			// 查找目标类
			targetClassName := relation.TargetClass
			targetClass := my.Nodes[targetClassName]
			if targetClass == nil {
				log.Warn().Str("class", class.Name).Str("field", field.Name).
					Str("targetClass", targetClassName).Msg("关系目标类不存在")
				continue
			}

			// 找到目标字段
			targetField := targetClass.Fields[relation.TargetField]
			if targetField == nil {
				log.Warn().Str("class", class.Name).Str("field", field.Name).
					Str("targetClass", targetClassName).Str("targetField", relation.TargetField).
					Msg("关系目标字段不存在")
				continue
			}

			// 如果目标字段没有反向关系，创建一个
			if targetField.Relation == nil {
				targetField.Relation = &internal.Relation{
					SourceClass: targetClass.Name,
					SourceField: targetField.Name,
					TargetClass: class.Name,
					TargetField: field.Name,
					Type:        relation.Type.Reverse(),
					Reverse:     relation,
				}
			}

			// 链接反向关系
			relation.Reverse = targetField.Relation

			// 根据关系类型收集需要创建的字段信息
			switch relation.Type {
			case internal.MANY_TO_MANY:
				// 添加多对多关系字段
				relName := my.uniqueFieldName(class, strcase.ToLowerCamel(inflection.Plural(targetClassName)))
				desc := createDescription(targetClassName, true)
				addRelationField(class.Name, targetClassName, true, false, false, false,
					internal.MANY_TO_MANY, relName, desc)

				// 处理中间表
				if relation.Through != nil {
					// 确保中间表类名和字段信息正确
					if relation.Through.Name == "" {
						if relation.Through.Table != "" {
							relation.Through.Name = relation.Through.Table
							log.Debug().Str("table", relation.Through.Table).Str("className", relation.Through.Name).
								Msg("从表名自动推导中间表类名")
						} else {
							relation.Through.Name = class.Name + targetClass.Name
							log.Debug().Str("sourceClass", class.Name).Str("targetClass", targetClass.Name).
								Str("throughClass", relation.Through.Name).Msg("从关联类名组合中间表类名")
						}
					}

					// 确保Fields字段不为空
					if relation.Through.Fields == nil {
						relation.Through.Fields = make(map[string]*internal.Field)
					}

					// 从 Nodes 中查找表对应的类并添加中间表关系
					if throughClass := my.Nodes[relation.Through.Table]; throughClass != nil {
						throughFieldName := my.uniqueFieldName(class, strcase.ToLowerCamel(inflection.Plural(throughClass.Name)))
						throughDesc := createDescription(throughClass.Name, true)
						addRelationField(class.Name, throughClass.Name, true, false, false, true,
							internal.MANY_TO_MANY, throughFieldName, throughDesc)
					}
				}

			case internal.ONE_TO_MANY:
				// 添加一对多关系字段
				relName := my.uniqueFieldName(class, strcase.ToLowerCamel(inflection.Plural(targetClassName)))
				desc := createDescription(targetClassName, true)
				addRelationField(class.Name, targetClassName, true, false, false, false,
					internal.ONE_TO_MANY, relName, desc)

			case internal.MANY_TO_ONE:
				// 添加多对一关系字段
				relName := my.uniqueFieldName(class, strcase.ToLowerCamel(targetClassName))
				desc := createDescription(targetClassName, false)
				addRelationField(class.Name, targetClassName, false, field.Nullable, false, false,
					internal.MANY_TO_ONE, relName, desc)

				// 收集反向关系字段信息（一对多）
				// 创建唯一的键来防止重复
				reverseKey := targetClassName + ":" + class.Name
				if !reverseRelationKeys[reverseKey] {
					reverseName := my.uniqueFieldName(targetClass, strcase.ToLowerCamel(inflection.Plural(className)))
					reverseDesc := createDescription(className, true)
					addRelationField(targetClassName, class.Name, true, false, true, false,
						internal.ONE_TO_MANY, reverseName, reverseDesc)
					reverseRelationKeys[reverseKey] = true
				}

			case internal.RECURSIVE:
				// 处理递归关系
				if strings.HasSuffix(fieldName, "Id") || strings.HasSuffix(fieldName, "ID") {
					// 添加父级关系字段
					parentName := my.uniqueFieldName(class, "parent")
					parentDesc := "父" + className + "对象"
					addRelationField(class.Name, className, false, true, false, false,
						internal.RECURSIVE, parentName, parentDesc)

					// 添加子级关系字段
					childrenName := my.uniqueFieldName(targetClass, "children")
					childrenDesc := "子" + className + "列表"
					addRelationField(className, className, true, false, false, false,
						internal.RECURSIVE, childrenName, childrenDesc)
				}
			}
		}
	}

	// 第二阶段：创建所有关系字段
	for _, info := range fieldsToCreate {
		if class := my.Nodes[info.SourceClass]; class != nil {
			// 如果字段不存在，则创建
			if _, has := class.Fields[info.FieldName]; !has {
				class.Fields[info.FieldName] = &internal.Field{
					Type:        info.TargetClass,
					Name:        info.FieldName,
					Virtual:     true,
					IsList:      info.IsList,
					Nullable:    info.Nullable,
					IsThrough:   info.IsThrough,
					Description: info.Description,
				}
			}
		}
	}

	log.Debug().Msg("关系处理和字段创建完成")
}

// uniqueFieldName 确保字段名在类中唯一
func (my *Metadata) uniqueFieldName(class *internal.Class, baseName string) string {
	fieldName := baseName
	counter := 1

	// 直接检查字段是否存在
	for class.Fields[fieldName] != nil {
		fieldName = baseName + strconv.Itoa(counter)
		counter++
	}

	return fieldName
}

// saveToFile 保存元数据到文件
func (my *Metadata) saveToFile(filePath string) error {
	log.Info().Str("file", filePath).Msg("开始保存元数据到文件")

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("创建目录失败")
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 使用自定义序列化为JSON
	data, err := json.MarshalIndent(my, "", "  ")
	if err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("序列化元数据失败")
		return fmt.Errorf("序列化元数据失败: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		log.Error().Err(err).Str("file", filePath).Msg("写入元数据文件失败")
		return fmt.Errorf("写入元数据文件失败: %w", err)
	}

	log.Info().Int("classes", len(my.Nodes)).Msg("保存元数据到文件完成")
	return nil
}
