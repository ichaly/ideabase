package protocol

import (
	"sort"
	"strconv"
	"strings"
)

type TableKind string

const (
	TableBase         TableKind = "table"
	TablePartitioned  TableKind = "partitioned"
	TableView         TableKind = "view"
	TableMaterialized TableKind = "materialized"
	TableForeign      TableKind = "foreign"
)

func (kind TableKind) ReadOnly() bool {
	return kind == TableView || kind == TableMaterialized || kind == TableForeign
}

type ConstraintKind string

const (
	ConstraintPrimary   ConstraintKind = "primary"
	ConstraintUnique    ConstraintKind = "unique"
	ConstraintForeign   ConstraintKind = "foreign"
	ConstraintCheck     ConstraintKind = "check"
	ConstraintExclusion ConstraintKind = "exclusion"
)

// Constraint preserves column order because composite keys are positional.
type Constraint struct {
	Name              string         `json:"name"`
	Kind              ConstraintKind `json:"kind"`
	Columns           []string       `json:"columns,omitempty"`
	ReferencedTable   string         `json:"referencedTable,omitempty"`
	ReferencedColumns []string       `json:"referencedColumns,omitempty"`
	Expression        string         `json:"expression,omitempty"`
	Deferrable        bool           `json:"deferrable,omitempty"`
	Validated         bool           `json:"validated"`
	OnUpdate          string         `json:"onUpdate,omitempty"`
	OnDelete          string         `json:"onDelete,omitempty"`
	Match             string         `json:"match,omitempty"`
}

func (constraint *Constraint) IsKey() bool {
	return constraint != nil && (constraint.Kind == ConstraintPrimary || constraint.Kind == ConstraintUnique) && len(constraint.Columns) > 0
}

// IsConflictTarget reports whether PostgreSQL can use the constraint as an
// ON CONFLICT arbiter. PostgreSQL rejects deferrable unique constraints here.
func (constraint *Constraint) IsConflictTarget() bool {
	return constraint.IsKey() && !constraint.Deferrable
}

type IndexKey struct {
	Column     string `json:"column,omitempty"`
	Expression string `json:"expression,omitempty"`
}

type Index struct {
	Name      string     `json:"name"`
	Keys      []IndexKey `json:"keys"`
	Unique    bool       `json:"unique,omitempty"`
	Primary   bool       `json:"primary,omitempty"`
	Invalid   bool       `json:"invalid,omitempty"`
	Deferred  bool       `json:"deferred,omitempty"`
	Predicate string     `json:"predicate,omitempty"`
	Method    string     `json:"method,omitempty"`
	Include   []string   `json:"include,omitempty"`
}

func (index *Index) IsUsableConflictTarget() bool {
	if index == nil || !index.Unique || index.Invalid || index.Deferred || index.Predicate != "" || len(index.Keys) == 0 {
		return false
	}
	for _, key := range index.Keys {
		if key.Column == "" || key.Expression != "" {
			return false
		}
	}
	return true
}

type Key struct {
	Name    string
	Columns []string
	Primary bool
}

// PrimaryKeyFields returns the explicit ordered key, with an IsPrimary
// fallback for legacy programmatic metadata that predates Class.PrimaryKeys.
func (class *Class) PrimaryKeyFields() []string {
	if class == nil {
		return nil
	}
	if len(class.PrimaryKeys) > 0 {
		return append([]string(nil), class.PrimaryKeys...)
	}
	var names []string
	for name, field := range class.Fields {
		if field != nil && name == field.Name && field.IsPrimary {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// ConflictTargets returns stable, deduplicated, globally usable keys. Labels
// are protocol-owned so Renderer, authorization, and SQL cannot drift.
func (class *Class) ConflictTargets() []Key {
	if class == nil {
		return nil
	}
	var keys []Key
	seen := make(map[string]bool)
	add := func(name string, columns []string, primary bool) {
		identity := strings.Join(columns, "\x00")
		if len(columns) == 0 || seen[identity] {
			return
		}
		seen[identity] = true
		keys = append(keys, Key{Name: name, Columns: append([]string(nil), columns...), Primary: primary})
	}
	for _, constraint := range class.Constraints {
		if constraint.IsConflictTarget() {
			add(constraint.Name, constraint.Columns, constraint.Kind == ConstraintPrimary)
		}
	}
	for _, index := range class.Indexes {
		if !index.IsUsableConflictTarget() {
			continue
		}
		columns := make([]string, len(index.Keys))
		for i, key := range index.Keys {
			columns[i] = key.Column
		}
		add(index.Name, columns, index.Primary)
	}
	// Explicit legacy/config facts remain a compatibility input only when no
	// first-class catalog exists. Otherwise they could accidentally turn a
	// deferrable or partial uniqueness fact into a globally valid arbiter.
	if len(class.Constraints) == 0 && len(class.Indexes) == 0 {
		add("primary", class.PrimaryKeyFields(), true)
		var fieldNames []string
		for name, field := range class.Fields {
			if field != nil && name == field.Name && field.IsUnique {
				fieldNames = append(fieldNames, name)
			}
		}
		sort.Strings(fieldNames)
		for _, name := range fieldNames {
			add(name, []string{name}, false)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Primary != keys[j].Primary {
			return keys[i].Primary
		}
		return keys[i].Name < keys[j].Name
	})
	unique := 0
	for i := range keys {
		if keys[i].Primary {
			keys[i].Name = "PRIMARY"
		} else {
			unique++
			keys[i].Name = "UNIQUE_" + strconv.Itoa(unique)
		}
	}
	return keys
}

func (class *Class) PrimaryConflictTarget() (Key, bool) {
	for _, key := range class.ConflictTargets() {
		if key.Primary {
			return key, true
		}
	}
	return Key{}, false
}

func (class *Class) ConflictTarget(name string) (Key, bool) {
	for _, key := range class.ConflictTargets() {
		if key.Name == name {
			return key, true
		}
	}
	return Key{}, false
}

type TypeKind string

const (
	TypeBase   TypeKind = "base"
	TypeDomain TypeKind = "domain"
	TypeEnum   TypeKind = "enum"
	TypeArray  TypeKind = "array"
)

type TypeRef struct {
	Schema     string   `json:"schema,omitempty"`
	Name       string   `json:"name"`
	Kind       TypeKind `json:"kind"`
	Base       string   `json:"base,omitempty"`
	Element    *TypeRef `json:"element,omitempty"`
	EnumLabels []string `json:"enumLabels,omitempty"`
}

type GenerationKind string

const (
	GeneratedIdentityAlways    GenerationKind = "identity-always"
	GeneratedIdentityByDefault GenerationKind = "identity-by-default"
	GeneratedStored            GenerationKind = "stored"
)

type ColumnDefault struct {
	Expression string `json:"expression"`
}

type ColumnGeneration struct {
	Kind       GenerationKind `json:"kind"`
	Expression string         `json:"expression,omitempty"`
}

// Column groups database-only facts instead of growing generic Field flags.
type Column struct {
	Ordinal    int               `json:"ordinal"`
	Default    *ColumnDefault    `json:"default,omitempty"`
	Generation *ColumnGeneration `json:"generation,omitempty"`
	Type       TypeRef           `json:"type"`
}
