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
	"github.com/ichaly/ideabase/utl"
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
	k.SetDefault("schema.max-depth", 20)
	k.SetDefault("schema.introspection", true)
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
	// 所有Loader执行完仍无任何元数据：生产环境直接报错避免带空schema启动，debug模式保留宽松行为便于排查
	if len(my.Nodes) == 0 && !cfg.IsDebug() {
		return nil, fmt.Errorf("元数据加载失败：所有加载器执行后无任何实体（请检查数据库连接、metadata.file 或 metadata.classes 配置）")
	}
	// 进行驼峰命名和过滤处理
	my.normalize()
	// codec标量名与实体类名冲突在构建期拦截（scalar与type同名，起服务时schema必然加载失败）
	for _, codec := range my.codecs {
		if class, ok := my.Nodes[codec.Name()]; ok && class.Name == codec.Name() {
			return nil, fmt.Errorf("codec标量 %s 与实体类名冲突（scalar与type同名schema无法加载），请更换codec名或实体别名", codec.Name())
		}
	}
	// 字段级关系声明（config/旧格式文件）收进类级关系集合
	my.collectRelations()
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
			if name != field.Name || field.Virtual || my.configured(class, field) {
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
// normalize后类名/字段名已规范化，而配置原文键可能是表名/列名等原始名，需多键尝试匹配
func (my *Metadata) configured(class *protocol.Class, field *protocol.Field) bool {
	if my.cfg == nil {
		return false
	}
	for _, classKey := range []string{class.Name, class.Table} {
		classConfig, ok := my.cfg.Metadata.Classes[classKey]
		if classKey == "" || !ok {
			continue
		}
		for _, fieldKey := range []string{field.Name, field.Column} {
			if fieldKey == "" {
				continue
			}
			if fieldConfig, ok := classConfig.Fields[fieldKey]; ok && fieldConfig.Type != "" {
				return true
			}
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

// SchemaName 返回数据库schema名（实现compiler.Schemer），空=不限定
func (my *Metadata) SchemaName() string {
	if my.cfg == nil {
		return ""
	}
	return my.cfg.Schema.Schema
}

// DefaultLimit 返回列表查询缺省LIMIT（实现compiler.Limiter），0=不注入
func (my *Metadata) DefaultLimit() int {
	if my.cfg == nil {
		return 0
	}
	return my.cfg.Schema.DefaultLimit
}

// MaxDepth 返回查询选择集最大嵌套深度，0=不限制
func (my *Metadata) MaxDepth() int {
	if my.cfg == nil {
		return 0
	}
	return my.cfg.Schema.MaxDepth
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

// relationField 收集阶段产出的关系虚拟字段：owner类上名为name、指向target的字段
type relationField struct {
	owner, target, name, description string
	isList, nullable, isThrough      bool
	relation                         *protocol.Relation // join元数据，编译器据此生成关联条件
}

// cloneRelation 复制关系元数据，reverse为true时交换源和目标方向
func cloneRelation(rel *protocol.Relation, relType protocol.RelationType, reverse bool) *protocol.Relation {
	result := &protocol.Relation{
		Type:         relType,
		Name:         rel.Name,
		SourceClass:  rel.SourceClass,
		SourceField:  rel.SourceField,
		SourceFields: rel.SourceFields,
		TargetClass:  rel.TargetClass,
		TargetField:  rel.TargetField,
		TargetFields: rel.TargetFields,
	}
	if reverse {
		result.SourceClass, result.TargetClass = result.TargetClass, result.SourceClass
		result.SourceField, result.TargetField = result.TargetField, result.SourceField
		result.SourceFields, result.TargetFields = result.TargetFields, result.SourceFields
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

// collectRelations 把字段级关系声明（config通道/旧格式文件的兼容指针）收进类级
// Relations集合：MANY_TO_ONE同时合成目标类的反向ONE_TO_MANY（与db加载器行为对齐），
// 递归只收外键侧方向；AddRelation按身份键去重，db已登记的不重复
func (my *Metadata) collectRelations() {
	for _, className := range utl.SortKeys(my.Nodes) {
		class := my.Nodes[className]
		if className != class.Name {
			continue
		}
		for _, fieldName := range utl.SortKeys(class.Fields) {
			field := class.Fields[fieldName]
			if fieldName != field.Name || field.Relation == nil || field.Virtual || field.Column == "" {
				continue
			}
			rel := field.Relation
			if rel.SourceClass == "" {
				rel.SourceClass = class.Name
			}
			if rel.SourceField == "" {
				rel.SourceField = field.Name
			}
			switch rel.Type {
			case protocol.MANY_TO_ONE:
				class.AddRelation(rel)
				if target := my.Nodes[rel.TargetClass]; target != nil {
					target.AddRelation(cloneRelation(rel, protocol.ONE_TO_MANY, true))
				}
			case protocol.ONE_TO_MANY, protocol.MANY_TO_MANY:
				class.AddRelation(rel)
			case protocol.RECURSIVE:
				if !field.IsPrimary { // 只收外键侧方向，主键侧指针是同一关系的镜像
					class.AddRelation(rel)
				}
			}
		}
	}
}

// processRelations 处理实体间关系：以类级Relations集合为唯一输入，
// 按关系类型收集待建的虚拟字段（每条关系恰好一个入口，天然无跨源查重），
// 同名碰撞按源列词干改名，再统一挂载到类
func (my *Metadata) processRelations() {
	log.Debug().Msg("处理所有关系信息")

	var pending []relationField

	// 排序遍历保证收集顺序确定，命名跨启动稳定
	for _, className := range utl.SortKeys(my.Nodes) {
		class := my.Nodes[className]
		// 跳过表名索引，只处理类名索引
		if className != class.Name {
			continue
		}
		relations := append([]*protocol.Relation(nil), class.Relations...)
		sort.Slice(relations, func(i, j int) bool { return relations[i].Key() < relations[j].Key() })
		for _, relation := range relations {
			targetClass := my.Nodes[relation.TargetClass]
			if targetClass == nil {
				log.Warn().Str("class", class.Name).Str("relation", relation.Key()).Msg("关系目标类不存在")
				continue
			}
			if targetClass.Fields[relation.TargetColumns()[0]] == nil {
				log.Warn().Str("class", class.Name).Str("relation", relation.Key()).Msg("关系目标字段不存在")
				continue
			}

			switch relation.Type {
			case protocol.MANY_TO_MANY:
				pending = append(pending, my.collectManyToMany(class, relation)...)
			case protocol.ONE_TO_MANY:
				pending = append(pending, my.listField(class, relation.TargetClass, false, relation))
			case protocol.MANY_TO_ONE:
				nullable := false
				if sf := class.Fields[relation.SourceColumns()[0]]; sf != nil {
					nullable = sf.Nullable
				}
				pending = append(pending, relationField{
					owner:       class.Name,
					target:      relation.TargetClass,
					name:        strcase.ToLowerCamel(relation.TargetClass),
					nullable:    nullable,
					description: "关联的" + relation.TargetClass,
					relation:    relation,
				})
			case protocol.RECURSIVE:
				pending = append(pending, my.collectRecursive(class, relation)...)
			}
		}
	}

	// 同名碰撞（同目标多关系/自引用多对多）按源列词干改名：author_id→author，
	// 反向列表 authorComments；改名后仍冲突由创建期后缀兜底
	renameCollisions(pending)

	// 创建期才定名：此时能看到同批已建字段，同名冲突自动后缀（收集期定名会静默丢字段）
	for _, f := range pending {
		class := my.Nodes[f.owner]
		if class == nil {
			continue
		}
		// 旧格式文件已含虚拟字段：同名同关系视为已生成（重载幂等，不再产出comments1幽灵）
		if exist := class.Fields[f.name]; exist != nil && exist.Virtual &&
			exist.Relation != nil && exist.Relation.Key() == f.relation.Key() {
			continue
		}
		name := my.uniqueFieldName(class, f.name)
		class.Fields[name] = &protocol.Field{
			Type:        f.target,
			Name:        name,
			Virtual:     true,
			IsList:      f.isList,
			Nullable:    f.nullable,
			IsThrough:   f.isThrough,
			Description: f.description,
			Relation:    f.relation,
		}
	}

	log.Debug().Msg("关系处理和字段创建完成")
}

// renameCollisions 同一类下同名的待建字段按关系源列词干改名（单关系场景零改动）
func renameCollisions(pending []relationField) {
	byName := make(map[string][]int)
	for i, f := range pending {
		key := f.owner + "\x00" + f.name
		byName[key] = append(byName[key], i)
	}
	for _, group := range byName {
		if len(group) < 2 {
			continue
		}
		for _, i := range group {
			if name := stemName(&pending[i]); name != "" {
				pending[i].name = name
			}
		}
	}
}

// stemName 按关系键列词干派生字段名：author_id→author（正向）、
// authorComments（反向列表）、friends（多对多经中间表远端键）；无词干返回空串
func stemName(f *relationField) string {
	rel := f.relation
	if rel == nil {
		return ""
	}
	switch rel.Type {
	case protocol.MANY_TO_ONE:
		return stem(rel.SourceColumns()[0])
	case protocol.ONE_TO_MANY:
		// 反向列表：外键列在目标侧（TargetField），词干+复数目标类
		if s := stem(rel.TargetColumns()[0]); s != "" {
			return strcase.ToLowerCamel(s + "_" + inflection.Plural(f.target))
		}
	case protocol.MANY_TO_MANY:
		if rel.Through != nil {
			if s := stem(rel.Through.TargetKey); s != "" {
				return strcase.ToLowerCamel(inflection.Plural(s))
			}
		}
	}
	return ""
}

// stem 键列词干：去掉 _id/Id 后缀转小驼峰；列名即id等无词干时返回空串
func stem(column string) string {
	s := strings.TrimSuffix(strings.TrimSuffix(column, "_id"), "Id")
	if s == "" || s == column {
		return ""
	}
	return strcase.ToLowerCamel(s)
}

// listField 指向目标类的列表字段（一对多/多对多共用形态）；name为基础名，创建期唯一化
func (my *Metadata) listField(class *protocol.Class, target string, isThrough bool, rel *protocol.Relation) relationField {
	return relationField{
		owner:       class.Name,
		target:      target,
		name:        strcase.ToLowerCamel(inflection.Plural(target)),
		isList:      true,
		isThrough:   isThrough,
		description: "关联的" + target + "列表",
		relation:    rel,
	}
}

// collectManyToMany 多对多列表字段；有中间表时额外生成指向中间表的一对多字段
func (my *Metadata) collectManyToMany(class *protocol.Class, rel *protocol.Relation) []relationField {
	fields := []relationField{my.listField(class, rel.TargetClass, false, cloneRelation(rel, protocol.MANY_TO_MANY, false))}
	if through := rel.Through; through != nil {
		if throughClass := my.Nodes[through.TableName]; throughClass != nil {
			// 指向中间表本身是普通一对多：源类主键 -> 中间表的源外键
			fields = append(fields, my.listField(class, throughClass.Name, true, &protocol.Relation{
				Type:        protocol.ONE_TO_MANY,
				SourceClass: class.Name,
				SourceField: rel.SourceField,
				TargetClass: throughClass.Name,
				TargetField: through.SourceKey,
			}))
		}
	}
	return fields
}

// collectRecursive 自关联实体的parent/children直接关系 + descendants/ancestors全树字段
func (my *Metadata) collectRecursive(class *protocol.Class, rel *protocol.Relation) []relationField {
	deep := func(reverse bool) *protocol.Relation {
		r := cloneRelation(rel, protocol.RECURSIVE, reverse)
		r.Deep = true // 递归CTE全树遍历，depth参数限深
		return r
	}
	name := class.Name
	return []relationField{
		{owner: name, target: name, name: "parent", nullable: true,
			description: "父" + name + "对象", relation: cloneRelation(rel, protocol.RECURSIVE, false)},
		{owner: name, target: name, name: "children", isList: true,
			description: "子" + name + "列表", relation: cloneRelation(rel, protocol.RECURSIVE, true)},
		{owner: name, target: name, name: "descendants", isList: true,
			description: "全部后代（递归）", relation: deep(true)},
		{owner: name, target: name, name: "ancestors", isList: true,
			description: "全部祖先（递归）", relation: deep(false)},
	}
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
	classRelations := make([]*protocol.Relation, 0)

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
			if field.Column == "" {
				fields[fieldKey] = field
			} else {
				indexKeys(fields, fieldKey, field.Column, &field.Name, metadata.ConvertFieldName(field.Column, config), field)
			}
			if field.Relation != nil {
				relations = append(relations, field)
			}
		}
		class.Fields = fields
		classRelations = append(classRelations, class.Relations...)

		if class.Table == "" {
			nodes[classKey] = class
		} else {
			indexKeys(nodes, classKey, class.Table, &class.Name, metadata.ConvertClassName(class.Table, config), class)
		}
	}

	// 修正关系依赖中的类名（字段指针与类级集合可能指向同一对象，重命名幂等）
	rename := func(rel *protocol.Relation) {
		if node, ok := nodes[rel.SourceClass]; ok {
			rel.SourceClass = node.Name
		}
		if node, ok := nodes[rel.TargetClass]; ok {
			rel.TargetClass = node.Name
		}
	}
	for _, field := range relations {
		rename(field.Relation)
	}
	for _, rel := range classRelations {
		rename(rel)
	}

	my.Nodes = nodes
	return nil
}

// indexKeys 多键索引：同一对象在map中挂主名/原始名（表名或列名）/别名多个键指向自身，
// 每次按访问键补齐缺失键。类与字段的三重索引共用此逻辑：
//   - 访问键是原始名：主名未定名时先标准化，补主名键
//   - 主名即标准名（主对象）：补原始名键
//   - 别名访问：只补主名键，不占原始名键（避免别名对象覆盖主对象）
func indexKeys[T any](dst map[string]T, key, rawName string, name *string, canon string, obj T) {
	switch {
	case key == rawName:
		if *name == rawName {
			*name = canon
		}
		dst[*name] = obj
	case *name == canon:
		dst[rawName] = obj
	case key != *name:
		dst[*name] = obj
	}
	dst[key] = obj
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
