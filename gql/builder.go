package gql

import (
	"context"
	"fmt"
	"time"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"gorm.io/gorm"
)

// Builder is the sole assembly path for one immutable Engine.
type Builder struct {
	k         *std.Konfig
	database  *gorm.DB
	metadata  []MetadataOption
	items     []any
	replaces  []Resolver
	wraps     []resolverWrap
	documents []string
	observer  Observer
	policy    PolicyProvider
	security  SecurityStateProvider
	lease     time.Duration
	built     bool
}

// SubscriptionLease sets the periodic authorization refresh before Build.
func (my *Builder) SubscriptionLease(interval time.Duration) error {
	if my.built || interval <= 0 || my.lease != 0 {
		return fmt.Errorf("SubscriptionLease必须在Build前设置为正数")
	}
	my.lease = interval
	return nil
}

// Security installs the shared revocation boundary before Build.
func (my *Builder) Security(provider SecurityStateProvider) error {
	if my.built {
		return fmt.Errorf("builder已冻结：Security仅限Build前调用")
	}
	if provider == nil || my.security != nil {
		return fmt.Errorf("SecurityStateProvider必须且只能设置一次")
	}
	my.security = provider
	return nil
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

// Policy installs the sole policy provider for this engine. It must be set
// before Build; policy providers are runtime dependencies, not registrations.
func (my *Builder) Policy(provider PolicyProvider) error {
	if my.built {
		return fmt.Errorf("builder已冻结：Policy仅限Build前调用")
	}
	if provider == nil {
		return fmt.Errorf("PolicyProvider不能为空")
	}
	if my.policy != nil {
		return fmt.Errorf("PolicyProvider只能设置一次")
	}
	my.policy = provider
	return nil
}

// Register adds Resolver/Batch/Remote handlers before Build.
func (my *Builder) Register(items ...any) error {
	if my.built {
		return fmt.Errorf("builder已冻结：Register仅限Build前调用")
	}
	for _, item := range items {
		if _, ok := item.(PolicyProvider); ok {
			return fmt.Errorf("PolicyProvider必须通过Builder.Policy设置")
		}
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
	if my.policy != nil && hasMetadataPermissions(metadata) {
		return nil, fmt.Errorf("显式PolicyProvider与metadata permissions不能同时配置")
	}
	provider := my.policy
	if provider == nil {
		provider = metadataPolicy(metadata)
	}
	if provider == nil {
		return nil, fmt.Errorf("Engine policy provider or metadata permissions is required")
	}
	if my.security == nil && my.database != nil && my.database.Name() == "postgres" && metadata.cfg != nil && !metadata.cfg.IsDebug() {
		return nil, fmt.Errorf("production Engine requires an explicit SecurityStateProvider")
	}
	if err := validateProductionMigrations(metadata, my.database); err != nil {
		return nil, err
	}
	if err := configureSearchMode(metadata, my.database); err != nil {
		return nil, fmt.Errorf("detect search capability: %w", err)
	}
	// Loader-owned metadata is a build-time assembly object. Compile only
	// against the Engine-owned frozen catalog.
	catalog := metadata.freeze()
	compiler, err := NewCompiler(catalog, nil)
	if err != nil {
		return nil, err
	}
	renderer := NewRenderer(catalog)
	schema, err := loadSchema(catalog, renderer)
	if err != nil {
		return nil, err
	}
	executor, err := newEngine(my.database, catalog, compiler, schema)
	if err != nil {
		return nil, err
	}
	executor.observer = my.observer
	executor.policy = provider
	executor.policyBuiltin = my.policy == nil
	executor.revocation = my.security
	executor.subscriptionLease = my.lease
	ready := false
	defer func() {
		if !ready {
			executor.Close()
		}
	}()
	if len(my.items) > 0 {
		if err := executor.register(my.items...); err != nil {
			return nil, err
		}
	}
	for _, resolver := range my.replaces {
		if resolver == nil {
			return nil, fmt.Errorf("replace不接受nil Resolver")
		}
		if err := executor.replace(resolver); err != nil {
			return nil, err
		}
	}
	for _, wrap := range my.wraps {
		if wrap.middleware == nil {
			return nil, fmt.Errorf("wrap不接受nil Middleware: %s", wrap.coordinate)
		}
		if err := executor.wrap(wrap.coordinate, wrap.middleware); err != nil {
			return nil, err
		}
	}
	for _, dir := range my.documents {
		if err := executor.loadDocuments(dir); err != nil {
			return nil, err
		}
	}
	_, err = executor.Snapshot()
	if err != nil {
		return nil, err
	}
	ready = true
	return executor, nil
}

func validateProductionMigrations(metadata *Metadata, database *gorm.DB) error {
	if metadata == nil || metadata.cfg == nil || metadata.cfg.IsDebug() || database == nil || database.Name() != "postgres" {
		return nil
	}
	requiresAudit := false
	for _, spec := range metadata.operations {
		if spec.operation == protocol.OP_PURGE {
			requiresAudit = true
			break
		}
	}
	if !requiresAudit {
		return nil
	}
	return postgresPreflight(context.Background(), database, "gql_audit_outbox", func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO gql_audit_outbox
  (event_key, tenant, actor, action, resource, request_id, snapshot, policy_revision, affected_count)
VALUES ('gql-preflight-' || pg_backend_pid()::text || '-' || clock_timestamp()::text,
  'preflight', 'preflight', 'purge', 'preflight', 'preflight', 'preflight', 0, 0)`).Error
	})
}
