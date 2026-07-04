package gql

import (
	"fmt"
	"github.com/ichaly/ideabase/gql/internal"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/metadata"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std"
	"github.com/jinzhu/inflection"
	"github.com/samber/lo"
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
	Nodes   map[string]*protocol.Class `json:"nodes"`
	Version string                     `json:"version"`

	// 全文搜索能力（启动探测或配置指定）
	searchMode   string
	searchConfig string

	// 标量编解码器：slice保留注册序（认领优先级），index供热路径按名查找
	codecs []Codec
	index  map[string]Codec
}

// findCodec 按标量名查编解码器（同名后注册者生效，由map覆盖语义天然保证）
func (my *Metadata) findCodec(name string) Codec {
	return my.index[name]
}

// setCodecs 装配编解码器注册表（slice保留注册序，index供按名查找与同名覆盖）
func (my *Metadata) setCodecs(codecs []Codec) {
	my.codecs = codecs
	my.index = make(map[string]Codec, len(codecs))
	for _, codec := range codecs {
		my.index[codec.Name()] = codec
	}
}

// MetadataOption 用于自定义Loader注册与移除
type MetadataOption func(*metadataOptions)

type metadataOptions struct {
	loaders []protocol.Loader
	codecs  []Codec
}

// WithLoader 添加或替换Loader
func WithLoader(loader protocol.Loader) MetadataOption {
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

// HookedLoader 装饰器，支持beforeLoad,afterLoad钩子
type HookedLoader struct {
	protocol.Loader
	afterLoad, beforeLoad func(h protocol.Hoster) error
}

func (my *HookedLoader) Load(h protocol.Hoster) error {
	if my.beforeLoad != nil {
		if err := my.beforeLoad(h); err != nil {
			return err
		}
	}
	if err := my.Loader.Load(h); err != nil {
		return err
	}
	if my.afterLoad != nil {
		return my.afterLoad(h)
	}
	return nil
}

// NewMetadata 策略模式重构，支持Loader注册与优先级排序
func NewMetadata(k *std.Konfig, d *gorm.DB, opts ...MetadataOption) (*Metadata, error) {
	cfg := &internal.Config{Schema: internal.SchemaConfig{TypeMapping: protocol.DataTypes}}

	// 设置默认配置
	k.SetDefault("schema.schema", "public")
	k.SetDefault("schema.default-limit", 10)
	k.SetDefault("schema.table-prefix", []string{})
	k.SetDefault("schema.exclude-tables", []string{})
	k.SetDefault("schema.exclude-fields", []string{})

	// 设置元数据默认配置
	k.SetDefault("metadata.file", "cfg/metadata.{mode}.json")
	k.SetDefault("metadata.use-camel", true)
	k.SetDefault("metadata.use-singular", true)
	k.SetDefault("metadata.show-through", true)

	if err := k.Unmarshal(cfg); err != nil {
		return nil, err
	}

	my := &Metadata{
		k: k, db: d, cfg: cfg,
		Nodes:   make(map[string]*protocol.Class),
		Version: time.Now().Format("20060102150405"),
		// 配置显式指定的搜索模式立即生效；为空时由NewExecutor启动探测补全
		searchMode:   cfg.Search.Mode,
		searchConfig: cfg.Search.Config,
	}

	// 默认Loader注册，Pgsql和Mysql用HookedLoader包装，dev模式下自动保存
	after := func(h protocol.Hoster) error {
		if cfg.IsDebug() {
			return my.saveToFile(metadata.ResolveMetadataPath(cfg))
		}
		return nil
	}
	defaultLoaders := []protocol.Loader{
		&HookedLoader{Loader: metadata.NewPgsqlLoader(cfg, d), afterLoad: after},
		&HookedLoader{Loader: metadata.NewMysqlLoader(cfg, d), afterLoad: after},
		metadata.NewFileLoader(cfg),
		metadata.NewConfigLoader(cfg),
	}
	options := &metadataOptions{loaders: defaultLoaders}
	// 应用自定义选项
	for _, opt := range opts {
		opt(options)
	}
	my.setCodecs(options.codecs)
	// 按优先级排序
	loaders := options.loaders
	if len(loaders) > 1 {
		sort.Slice(loaders, func(i, j int) bool {
			return loaders[i].Priority() < loaders[j].Priority()
		})
	}

	// 依次执行Loader
	for _, loader := range loaders {
		if loader.Support() {
			if err := loader.Load(my); err != nil {
				log.Warn().Err(err).Str("loader", loader.Name()).Msg("加载器执行失败")
			}
		}
	}
	// 进行驼峰命名和过滤处理
	my.normalize()
	// 统一关系处理
	my.processRelations()
	// 类型定型：结构推导 + Codec认领
	my.finalize()
	return my, nil
}

// finalize 元数据定型，field.Type自此成为schema类型的唯一真相：
//  1. 结构推导：主键与外键实列固定为ID标量（关系载体是Virtual字段不受影响），
//     使加解密与精度处理覆盖全部主外键；
//  2. Codec认领：实现了Matcher的codec把命中字段改写为对应标量。
//
// 字段级配置显式指定类型时保留配置（配置是最终裁决的例外通道）
func (my *Metadata) finalize() {
	for className, class := range my.Nodes {
		if className != class.Name {
			continue
		}
		for name, field := range class.Fields {
			if name != field.Name || field.Virtual || my.configured(className, name) {
				continue
			}
			if field.IsPrimary || field.Relation != nil {
				field.Type = protocol.SCALAR_ID
				continue
			}
			for _, codec := range my.codecs {
				if my.index[codec.Name()] != codec {
					continue // 同名被后注册者覆盖，认领一并让位
				}
				if m, ok := codec.(Matcher); ok && m.Match(class, field) {
					field.Type = codec.Name()
					break
				}
			}
		}
	}
}

// configured 字段类型是否被配置显式指定
func (my *Metadata) configured(className, fieldName string) bool {
	if class, ok := my.cfg.Metadata.Classes[className]; ok {
		if field, ok := class.Fields[fieldName]; ok {
			return field.Type != ""
		}
	}
	return false
}

func (my *Metadata) PutNode(className string, node *protocol.Class) error {
	if node == nil || node.Name == "" {
		return nil
	}
	my.Nodes[className] = node
	return nil
}

// 配置类型公开别名：实体/字段的代码内配置（k.Set("metadata.classes", ...)）
// internal包外部模块不可import，公共API经此别名暴露
type (
	ClassConfig = internal.ClassConfig
	FieldConfig = internal.FieldConfig
)

// SetSearchMode 记录探测/配置得到的全文搜索能力（NewExecutor启动时写入）
func (my *Metadata) SetSearchMode(mode, config string) {
	my.searchMode, my.searchConfig = mode, config
}

// SearchMode 返回全文搜索模式与分词配置（实现compiler.Searcher）
func (my *Metadata) SearchMode() (string, string) {
	return my.searchMode, my.searchConfig
}

func (my *Metadata) GetNode(name string) (*protocol.Class, bool) {
	n, ok := my.Nodes[name]
	return n, ok
}

func (my *Metadata) SetVersion(version string) {
	my.Version = version
}

// MarshalJSON 自定义JSON序列化
func (my *Metadata) MarshalJSON() ([]byte, error) {
	// 仅导出key和类名相同的节点
	nodes := make(map[string]*protocol.Class)
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

// processRelations 处理实体间的关系，包含两个阶段：
// 1. 收集阶段：遍历所有节点，收集需要处理的关系信息
//   - 处理各种关系类型（一对多、多对一、多对多、递归关系）
//   - 处理双向关系引用
//   - 处理中间表关系
//
// 2. 创建阶段：根据收集的信息创建关系字段
//   - 创建虚拟字段作为关系的载体
//   - 确保字段名唯一性
//   - 维护双向关系引用
func (my *Metadata) processRelations() {
	log.Debug().Msg("处理所有关系信息")

	// 定义关系字段信息结构体
	type RelationFieldInfo struct {
		SourceClass string
		TargetClass string
		FieldName   string
		IsList      bool
		Nullable    bool
		Description string
		IsThrough   bool
		Relation    *protocol.Relation // 关系字段的join元数据，编译器据此生成关联条件
	}

	// 存储所有需要创建的关系字段
	fieldsToCreate := make([]RelationFieldInfo, 0)
	// 用于避免重复创建反向关系字段的映射
	reverseRelationKeys := make(map[string]bool)

	// 添加关系字段信息的辅助函数
	addRelationField := func(sourceClass, targetClass string, isList, nullable, isThrough bool,
		fieldName string, description string, rel *protocol.Relation) {

		fieldsToCreate = append(fieldsToCreate, RelationFieldInfo{
			SourceClass: sourceClass,
			TargetClass: targetClass,
			FieldName:   fieldName,
			IsList:      isList,
			Nullable:    nullable,
			Description: description,
			IsThrough:   isThrough,
			Relation:    rel,
		})
	}

	// 复制关系元数据的辅助函数，reverse为true时交换源和目标方向
	cloneRelation := func(rel *protocol.Relation, relType protocol.RelationType, reverse bool) *protocol.Relation {
		result := &protocol.Relation{
			Type:        relType,
			SourceClass: rel.SourceClass,
			SourceField: rel.SourceField,
			TargetClass: rel.TargetClass,
			TargetField: rel.TargetField,
		}
		if reverse {
			result.SourceClass, result.TargetClass = result.TargetClass, result.SourceClass
			result.SourceField, result.TargetField = result.TargetField, result.SourceField
		}
		if rel.Through != nil {
			through := *rel.Through
			if reverse {
				through.SourceKey, through.TargetKey = through.TargetKey, through.SourceKey
			}
			result.Through = &through
		}
		return result
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

			// 根据关系类型收集需要创建的字段信息
			switch relation.Type {
			case protocol.MANY_TO_MANY:
				// 添加多对多关系字段
				relName := my.uniqueFieldName(class, strcase.ToLowerCamel(inflection.Plural(targetClassName)))
				desc := createDescription(targetClassName, true)
				addRelationField(class.Name, targetClassName, true, false, false,
					relName, desc, cloneRelation(relation, protocol.MANY_TO_MANY, false))

				// 处理中间表
				if relation.Through != nil {
					// 从 Nodes 中查找表对应的类并添加中间表关系
					if throughClass := my.Nodes[relation.Through.TableName]; throughClass != nil {
						throughFieldName := my.uniqueFieldName(class, strcase.ToLowerCamel(inflection.Plural(throughClass.Name)))
						throughDesc := createDescription(throughClass.Name, true)
						// 指向中间表本身是普通一对多：源类主键 -> 中间表的源外键
						addRelationField(class.Name, throughClass.Name, true, false, true,
							throughFieldName, throughDesc, &protocol.Relation{
								Type:        protocol.ONE_TO_MANY,
								SourceClass: class.Name,
								SourceField: relation.SourceField,
								TargetClass: throughClass.Name,
								TargetField: relation.Through.SourceKey,
							})
					}
				}

			case protocol.ONE_TO_MANY:
				// 添加一对多关系字段
				relName := my.uniqueFieldName(class, strcase.ToLowerCamel(inflection.Plural(targetClassName)))
				desc := createDescription(targetClassName, true)
				addRelationField(class.Name, targetClassName, true, false, false,
					relName, desc, cloneRelation(relation, protocol.ONE_TO_MANY, false))

			case protocol.MANY_TO_ONE:
				// 添加多对一关系字段
				relName := my.uniqueFieldName(class, strcase.ToLowerCamel(targetClassName))
				desc := createDescription(targetClassName, false)
				addRelationField(class.Name, targetClassName, false, field.Nullable, false,
					relName, desc, cloneRelation(relation, protocol.MANY_TO_ONE, false))

				// 收集反向关系字段信息（一对多）
				// 创建唯一的键来防止重复
				reverseKey := targetClassName + ":" + class.Name
				if !reverseRelationKeys[reverseKey] {
					reverseName := my.uniqueFieldName(targetClass, strcase.ToLowerCamel(inflection.Plural(className)))
					reverseDesc := createDescription(className, true)
					addRelationField(targetClassName, class.Name, true, false, false,
						reverseName, reverseDesc, cloneRelation(relation, protocol.ONE_TO_MANY, true))
					reverseRelationKeys[reverseKey] = true
				}

			case protocol.RECURSIVE:
				// 处理递归关系
				if strings.HasSuffix(fieldName, "Id") || strings.HasSuffix(fieldName, "ID") {
					// 添加父级关系字段：本类外键 -> 本类主键
					parentName := my.uniqueFieldName(class, "parent")
					parentDesc := "父" + className + "对象"
					addRelationField(class.Name, className, false, true, false,
						parentName, parentDesc, cloneRelation(relation, protocol.RECURSIVE, false))

					// 添加子级关系字段：本类主键 -> 本类外键
					childrenName := my.uniqueFieldName(targetClass, "children")
					childrenDesc := "子" + className + "列表"
					addRelationField(className, className, true, false, false,
						childrenName, childrenDesc, cloneRelation(relation, protocol.RECURSIVE, true))

					// 深度递归字段：全树后代/祖先（递归CTE，depth参数限深）
					descendants := cloneRelation(relation, protocol.RECURSIVE, true)
					descendants.Deep = true
					addRelationField(className, className, true, false, false,
						my.uniqueFieldName(class, "descendants"), "全部后代（递归）", descendants)

					ancestors := cloneRelation(relation, protocol.RECURSIVE, false)
					ancestors.Deep = true
					addRelationField(className, className, true, false, false,
						my.uniqueFieldName(class, "ancestors"), "全部祖先（递归）", ancestors)
				}
			}
		}
	}

	// 第二阶段：创建所有关系字段
	for _, info := range fieldsToCreate {
		if class := my.Nodes[info.SourceClass]; class != nil {
			// 如果字段不存在，则创建
			if _, has := class.Fields[info.FieldName]; !has {
				class.Fields[info.FieldName] = &protocol.Field{
					Type:        info.TargetClass,
					Name:        info.FieldName,
					Virtual:     true,
					IsList:      info.IsList,
					Nullable:    info.Nullable,
					IsThrough:   info.IsThrough,
					Description: info.Description,
					Relation:    info.Relation,
				}
			}
		}
	}

	log.Debug().Msg("关系处理和字段创建完成")
}

// uniqueFieldName 确保字段名在类中唯一
func (my *Metadata) uniqueFieldName(class *protocol.Class, baseName string) string {
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

// normalize 标准化元数据，包含两个核心功能：
// 1. 命名规范化：
//   - 根据配置决定是否启用驼峰命名
//   - 表名转换为大驼峰（如：users -> User）
//   - 字段名转换为小驼峰（如：user_name -> userName）
//   - 支持表名前缀过滤和单数化处理
//
// 2. 索引建立：
//   - 创建表名、类名和别名的索引,字段处理逻辑类似
//   - 确保可以通过表名、类名或别名快速查找
//   - 同时维护字段名、列名和别名的映射关系
func (my *Metadata) normalize() error {
	if my.cfg == nil {
		return nil
	}
	config := my.cfg.Metadata
	nodes := make(map[string]*protocol.Class)
	relations := make([]*protocol.Field, 0)

	for classKey, class := range my.Nodes {
		// 跳过需要忽略的表；白名单非空时未命中即排除，排除规则优先
		if class.Table != "" {
			if matchTables(config.ExcludeTables, class.Table) {
				continue
			}
			if len(config.IncludeTables) > 0 && !matchTables(config.IncludeTables, class.Table) {
				continue
			}
		}

		fields := make(map[string]*protocol.Field)
		for fieldKey, field := range class.Fields {
			// 跳过需要忽略的字段
			if field.Column != "" && lo.IndexOf(config.ExcludeFields, field.Column) > -1 {
				continue
			}
			// 如果是列索引且列名和字段名一致，则用标准名赋值并用标准名做key
			if field.Column != "" {
				canonName := metadata.ConvertFieldName(field.Column, config)
				if fieldKey == field.Column {
					if field.Name == field.Column {
						field.Name = canonName
					}
					fields[field.Name] = field
				} else if field.Name == canonName {
					fields[field.Column] = field
				} else if fieldKey != field.Name {
					fields[field.Name] = field
				}
			}
			// 始终用原始字段名做key
			fields[fieldKey] = field

			if field.Relation != nil {
				relations = append(relations, field)
			}
		}
		class.Fields = fields

		// 如果是表索引且表名和类名一致，则用标准名赋值并用标准名做key
		if class.Table != "" {
			canonName := metadata.ConvertClassName(class.Table, config)
			// classKey就三种情况：原始表名、标准类名、别名类名
			if classKey == class.Table {
				// 覆盖模式时类名和表名不一致就不用标准化类名,所以这里只处理类名使用了表名的情况
				if class.Name == class.Table {
					class.Name = canonName // 标准化类名
				}
				// 用标准化类名做key，支持通过类名查找
				nodes[class.Name] = class
			} else if class.Name == canonName {
				// 用原始表名做key，支持通过表名查找
				nodes[class.Table] = class
			} else if classKey != class.Name {
				// 用标准化类名做key，确保所有标准化名都能索引到
				nodes[class.Name] = class
			}
		}
		// 始终用原始类名做key
		nodes[classKey] = class
	}

	// 修正关系依赖中的类名
	for _, field := range relations {
		if node, ok := nodes[field.Relation.SourceClass]; ok {
			field.Relation.SourceClass = node.Name
		}
		if node, ok := nodes[field.Relation.TargetClass]; ok {
			field.Relation.TargetClass = node.Name
		}
	}

	my.Nodes = nodes
	return nil
}

// matchTables 表名匹配：精确或尾部*前缀通配（如 bot_*）
func matchTables(patterns []string, table string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(table, prefix) {
				return true
			}
		} else if p == table {
			return true
		}
	}
	return false
}
