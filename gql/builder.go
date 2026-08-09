package gql

import (
	"fmt"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std"
	"gorm.io/gorm"
)

// Builder assembles one immutable Engine. New applications use this entry;
// legacy constructors remain compatibility facades over the same components.
type Builder struct {
	k         *std.Konfig
	database  *gorm.DB
	metadata  []MetadataOption
	items     []any
	replaces  []Resolver
	wraps     []resolverWrap
	documents []string
	observer  Observer
	trusted   bool
	built     bool
}

// Observe installs a low-cardinality observer before Build.
func (my *Builder) Observe(observer Observer) error {
	if my.built {
		return fmt.Errorf("builder已冻结：Observe仅限Build前调用")
	}
	my.observer = observer
	return nil
}

type resolverWrap struct {
	coordinate string
	middleware ResolverMiddleware
}

// New creates the minimal database-to-GraphQL builder.
func New(k *std.Konfig, database *gorm.DB, options ...MetadataOption) *Builder {
	return &Builder{k: k, database: database, metadata: options}
}

// TrustedLocal explicitly opts into the legacy trusted-boundary policy.
// Production multi-tenant engines should configure per-class permissions.
func (my *Builder) TrustedLocal() *Builder {
	my.trusted = true
	return my
}

// Register adds Resolver/Batch/Remote handlers before Build.
func (my *Builder) Register(items ...any) error {
	if my.built {
		return fmt.Errorf("builder已冻结：Register仅限Build前调用")
	}
	my.items = append(my.items, items...)
	return nil
}

// Replace overrides generated SQL fields with explicit Resolver handlers.
func (my *Builder) Replace(resolvers ...Resolver) error {
	if my.built {
		return fmt.Errorf("builder已冻结：Replace仅限Build前调用")
	}
	my.replaces = append(my.replaces, resolvers...)
	return nil
}

// Wrap decorates a registered or replaced Resolver before the Engine freezes.
func (my *Builder) Wrap(coordinate string, middleware ResolverMiddleware) error {
	if my.built {
		return fmt.Errorf("builder已冻结：Wrap仅限Build前调用")
	}
	my.wraps = append(my.wraps, resolverWrap{coordinate: coordinate, middleware: middleware})
	return nil
}

// LoadDocuments records a persisted-operation directory to load during Build.
func (my *Builder) LoadDocuments(dirs ...string) error {
	if my.built {
		return fmt.Errorf("builder已冻结：LoadDocuments仅限Build前调用")
	}
	my.documents = append(my.documents, dirs...)
	return nil
}

// Engine is the immutable public runtime. Executor is embedded for API
// compatibility while Snapshot exposes its exact schema/capability contract.
type Engine struct {
	*Executor
}

// Build assembles metadata, compiler, schema, handlers and the immutable
// snapshot once. A failed build never returns a partially usable Engine.
func (my *Builder) Build() (*Engine, error) {
	if my.built {
		return nil, fmt.Errorf("builder已冻结：Build只能调用一次")
	}
	my.built = true
	if my.k == nil {
		return nil, fmt.Errorf("缺少配置")
	}
	metadata, err := NewMetadata(my.k, my.database, my.metadata...)
	if err != nil {
		return nil, err
	}
	if err := requirePolicies(metadata, my.trusted, "TrustedLocal"); err != nil {
		return nil, err
	}
	if my.trusted {
		log.Warn().Msg("已启用TrustedLocal；该边界不适用于多租户生产服务")
	}
	compiler, err := NewCompiler(metadata, nil)
	if err != nil {
		return nil, err
	}
	renderer := NewRenderer(metadata)
	schema, err := renderSchema(metadata, renderer)
	if err != nil {
		return nil, err
	}
	executor, err := newExecutor(my.database, metadata, compiler, schema)
	if err != nil {
		return nil, err
	}
	executor.observer = my.observer
	ready := false
	defer func() {
		if !ready {
			executor.Close()
		}
	}()
	if len(my.items) > 0 {
		if err := executor.Register(my.items...); err != nil {
			return nil, err
		}
	}
	for _, resolver := range my.replaces {
		if resolver == nil {
			return nil, fmt.Errorf("Replace不接受nil Resolver")
		}
		if err := executor.Replace(resolver); err != nil {
			return nil, err
		}
	}
	for _, wrap := range my.wraps {
		if wrap.middleware == nil {
			return nil, fmt.Errorf("Wrap不接受nil Middleware: %s", wrap.coordinate)
		}
		if err := executor.Wrap(wrap.coordinate, wrap.middleware); err != nil {
			return nil, err
		}
	}
	for _, dir := range my.documents {
		if err := executor.LoadDocuments(dir); err != nil {
			return nil, err
		}
	}
	_, err = executor.Snapshot()
	if err != nil {
		return nil, err
	}
	ready = true
	return &Engine{Executor: executor}, nil
}

func requirePolicies(metadata *Metadata, trusted bool, optIn string) error {
	if metadata == nil || trusted {
		return nil
	}
	for name, class := range metadata.Nodes {
		if class == nil || name != class.Name || class.Table == "" {
			continue
		}
		if len(class.Permissions) == 0 {
			return fmt.Errorf("实体%s缺少policy；仅可信边界可显式使用%s", class.Name, optIn)
		}
	}
	return nil
}
