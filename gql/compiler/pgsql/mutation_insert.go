package pgsql

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
)

type insertGroup struct {
	columns []string
	rows    []inputRow
}

// writeRelationInsertCTEs groups rows by their actual column set. Each group
// gets its own INSERT, so omitted columns keep their database defaults while
// every INSERT remains anchored to the parent CTE.
func (my *Dialect) writeRelationInsertCTEs(ctx *compiler.Context, class *protocol.Class, rows []inputRow, parent, returning string) ([]string, error) {
	groups, err := my.relationInsertGroups(class, rows)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(groups))
	for _, group := range groups {
		name := fmt.Sprintf("__c_%d", ctx.NextIndex())
		names = append(names, name)
		ctx.Write(`, `).Quote(name).Write(` AS (INSERT INTO `)
		tableRef(ctx, class.Table)
		ctx.Write(` (`)
		writeColumns(ctx, "", group.columns)
		ctx.Write(`) `)
		for i, row := range group.rows {
			if i > 0 {
				ctx.Write(` UNION ALL `)
			}
			ctx.Write(`SELECT `)
			if err := writeInsertRow(ctx, row, group.columns); err != nil {
				return nil, err
			}
			ctx.Write(` FROM `).Quote(parent)
		}
		if returning != "" {
			ctx.Write(` RETURNING `).Quote(returning)
		}
		ctx.Write(`)`)
	}
	return names, nil
}

func (my *Dialect) relationInsertGroups(class *protocol.Class, rows []inputRow) ([]insertGroup, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("input不能为空")
	}
	if err := my.applyGeneratedID(class, rows); err != nil {
		return nil, err
	}
	groups := make([]insertGroup, 0, len(rows))
	indices := make(map[string]int, len(rows))
	for _, row := range rows {
		columns := compactColumns(row.columns)
		shape := slices.Clone(columns)
		slices.Sort(shape)
		key := strings.Join(shape, "\x00")
		if index, ok := indices[key]; ok {
			groups[index].rows = append(groups[index].rows, row)
			continue
		}
		indices[key] = len(groups)
		groups = append(groups, insertGroup{columns: columns, rows: []inputRow{row}})
	}
	return groups, nil
}

func compactColumns(columns []string) []string {
	out := make([]string, 0, len(columns))
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, ok := seen[column]; ok {
			continue
		}
		seen[column] = struct{}{}
		out = append(out, column)
	}
	return out
}
