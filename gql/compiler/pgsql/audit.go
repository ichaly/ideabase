package pgsql

import (
	"strconv"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
)

// AuditOutboxDDL is the append-only PostgreSQL contract used by purge. Hosts
// apply it through their normal migration system; the engine never mutates
// production schema at startup.
const AuditOutboxDDL = `CREATE TABLE IF NOT EXISTS gql_audit_outbox (
  event_key text PRIMARY KEY,
  tenant text NOT NULL,
  actor text NOT NULL,
  action text NOT NULL,
  resource text NOT NULL,
  request_id text NOT NULL,
  snapshot text NOT NULL,
  policy_revision bigint NOT NULL,
  affected_count bigint NOT NULL,
  occurred_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  published_at timestamptz
)`

type PurgeAudit struct {
	Resource   string
	Source     string
	Occurrence int
}

// AuditWriter appends an audit fact to the mutation CTE graph. Implementations
// run inside the same PostgreSQL statement and therefore must not perform
// network I/O. External sinks consume the committed outbox asynchronously.
type AuditWriter interface {
	WritePurge(*compiler.Context, *Dialect, PurgeAudit)
}

type OutboxAuditWriter struct{ Table string }

func (writer OutboxAuditWriter) WritePurge(ctx *compiler.Context, dialect *Dialect, event PurgeAudit) {
	table := writer.Table
	if table == "" {
		table = "gql_audit_outbox"
	}
	request := dialect.Placeholder(ctx.AddRequiredContextSlot("requestId"))
	tenant := dialect.Placeholder(ctx.AddRequiredContextSlot("tenant"))
	actor := dialect.Placeholder(ctx.AddRequiredContextSlot("actor"))
	snapshot := dialect.Placeholder(ctx.AddRequiredContextSlot("snapshot"))
	revision := dialect.Placeholder(ctx.AddRequiredContextSlot("policyRevision"))
	ctx.Write(`, `).Quote("__gql_audit_", ctx.NextIndex()).Write(` AS (INSERT INTO `).Quote(table).Write(` (`)
	ctx.Write(`"event_key", "tenant", "actor", "action", "resource", "request_id", "snapshot", "policy_revision", "affected_count") SELECT `)
	ctx.Write(request, `::text || ':' || `, tenant, `::text || ':purge:`, event.Resource, `:`, strconv.Itoa(event.Occurrence), `', `, tenant, `, `, actor, `, 'purge', '`, event.Resource, `', `)
	ctx.Write(request, `, `, snapshot, `, `, revision, `, COUNT(*) FROM `).Quote(event.Source)
	ctx.Write(` HAVING COUNT(*) > 0 ON CONFLICT ("event_key") DO NOTHING RETURNING "event_key")`)
}

func (my *Dialect) buildPurgeAudit(ctx *compiler.Context, m *mutation) {
	ctx.MarkAudit()
	writer := my.Audit
	if writer == nil {
		writer = OutboxAuditWriter{}
	}
	source := m.cte
	if source == "" {
		source = m.class.Table
	}
	writer.WritePurge(ctx, my, PurgeAudit{Resource: m.class.Name, Source: source, Occurrence: m.occurrence})
}

// auditColumns is derived from the catalog's conventional column names. A
// partially present set is supported; only columns that exist are injected.
type auditColumns struct {
	createdAt, createdBy string
	updatedAt, updatedBy string
	deletedAt, deletedBy string
}

func auditColumnsFor(class *protocol.Class) auditColumns {
	var columns auditColumns
	for _, field := range sortedClassFields(class) {
		switch field.Column {
		case "created_at":
			columns.createdAt = field.Column
		case "created_by":
			columns.createdBy = field.Column
		case "updated_at":
			columns.updatedAt = field.Column
		case "updated_by":
			columns.updatedBy = field.Column
		case "deleted_at":
			columns.deletedAt = field.Column
		case "deleted_by":
			columns.deletedBy = field.Column
		}
	}
	return columns
}

func auditColumn(column string) bool {
	switch column {
	case "created_at", "created_by", "updated_at", "updated_by", "deleted_at", "deleted_by":
		return true
	default:
		return false
	}
}

func (my *Dialect) auditWriter(column string) paramWriter {
	return func(ctx *compiler.Context) error {
		if column == "created_by" || column == "updated_by" || column == "deleted_by" {
			ctx.Write(my.Placeholder(ctx.AddRequiredContextSlot("actor")))
		} else {
			ctx.Write(`CURRENT_TIMESTAMP`)
		}
		return nil
	}
}

// applyAudit adds server-owned audit values to input rows. It deliberately
// overwrites any pre-existing row writer so client values cannot win.
func (my *Dialect) applyAudit(class *protocol.Class, rows []inputRow, operation string) {
	columns := auditColumnsFor(class)
	var names []string
	switch operation {
	case protocol.OP_INSERT:
		names = []string{columns.createdAt, columns.createdBy, columns.updatedAt, columns.updatedBy}
	case protocol.OP_UPDATE:
		names = []string{columns.updatedAt, columns.updatedBy}
	}
	for _, column := range names {
		if column == "" {
			continue
		}
		for i := range rows {
			row := &rows[i]
			if _, ok := row.values[column]; !ok {
				row.columns = append(row.columns, column)
			}
			row.values[column] = my.auditWriter(column)
		}
	}
}

func (my *Dialect) writeAuditValue(ctx *compiler.Context, column string, null bool) error {
	if null {
		ctx.Write(`NULL`)
		return nil
	}
	return my.auditWriter(column)(ctx)
}

func (my *Dialect) appendAuditAssignments(ctx *compiler.Context, columns []string, null bool) error {
	for _, column := range columns {
		if column == "" {
			continue
		}
		ctx.Write(`, `).Quote(column).Write(` = `)
		if err := my.writeAuditValue(ctx, column, null); err != nil {
			return err
		}
	}
	return nil
}
