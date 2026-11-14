// Package schemigo provides declarative schema models and migration contracts.
package schemigo

// EntityKind identifies a class of schema objects for ordering independent changes.
type EntityKind string

const (
	// EntityExtensions identifies database extensions.
	EntityExtensions EntityKind = "extensions"
	// EntityEnums identifies enumerated types.
	EntityEnums EntityKind = "enums"
	// EntityTables identifies tables and their constraints.
	EntityTables EntityKind = "tables"
	// EntityFunctions identifies functions.
	EntityFunctions EntityKind = "functions"
	// EntityTriggers identifies triggers.
	EntityTriggers EntityKind = "triggers"
	// EntityViews identifies ordinary and materialized views.
	EntityViews EntityKind = "views"
)

// Schema describes one managed database namespace. Name qualifies object names;
// object names themselves are unqualified identifiers. Order gives preferences
// between independent changes; dependencies take precedence.
type Schema struct {
	Name       string       `json:"name"`
	Extensions []Extension  `json:"extensions"`
	Enums      []Enum       `json:"enums"`
	Tables     []Table      `json:"tables"`
	Functions  []Function   `json:"functions"`
	Triggers   []Trigger    `json:"triggers"`
	Views      []View       `json:"views"`
	Order      []EntityKind `json:"order"`
}

// Extension describes an installed extension and an optional pinned Version.
type Extension struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Enum describes an enumerated type with ordered Values.
type Enum struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Table describes columns and constraints. PrimaryKey preserves column order
// and cannot be combined with Column.PrimaryKey. Engine, OrderBy and PartitionBy
// are raw ClickHouse SQL clauses.
type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	PrimaryKey  []string     `json:"primary_key"`
	Indexes     []Index      `json:"indexes"`
	ForeignKeys []ForeignKey `json:"foreign_keys"`
	Checks      []Check      `json:"checks"`
	Engine      string       `json:"engine"`
	OrderBy     string       `json:"order_by"`
	PartitionBy string       `json:"partition_by"`
}

// Column describes a column. Type, Default and Using are raw dialect SQL;
// Using specifies the expression used when changing its type.
type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	NotNull    bool   `json:"not_null"`
	PrimaryKey bool   `json:"primary_key"`
	Default    string `json:"default"`
	Using      string `json:"using"`
}

// Index describes either Columns or a raw SQL Expression. Method defaults to
// btree; Where is raw SQL. Type and Granularity describe ClickHouse skipping indexes.
type Index struct {
	Name        string   `json:"name"`
	Columns     []string `json:"columns"`
	Expression  string   `json:"expression"`
	Unique      bool     `json:"unique"`
	Method      string   `json:"method"`
	Where       string   `json:"where"`
	Type        string   `json:"type"`
	Granularity int      `json:"granularity"`
}

// ForeignKey pairs Columns with ReferencedColumns in a managed ReferencedTable.
// OnDelete and OnUpdate are dialect referential actions.
type ForeignKey struct {
	Name              string   `json:"name"`
	Columns           []string `json:"columns"`
	ReferencedTable   string   `json:"referenced_table"`
	ReferencedColumns []string `json:"referenced_columns"`
	OnDelete          string   `json:"on_delete"`
	OnUpdate          string   `json:"on_update"`
}

// Check describes a named constraint with a raw SQL Expression.
type Check struct {
	Name       string `json:"name"`
	Expression string `json:"expression"`
}

// Function describes a SQL function. Parameters, Returns and Body are raw SQL;
// Parameters is the dialect's argument signature and distinguishes overloads.
type Function struct {
	Name       string `json:"name"`
	Parameters string `json:"parameters"`
	Returns    string `json:"returns"`
	Language   string `json:"language"`
	Body       string `json:"body"`
}

// Trigger describes a trigger on a managed table or ordinary view calling a managed Function.
// When and Event are dialect clauses; Condition is raw SQL. ForEach is ROW
// (the default) or STATEMENT and is authoritative over the legacy ForEachRow.
type Trigger struct {
	Name       string `json:"name"`
	When       string `json:"when"`
	Event      string `json:"event"`
	Table      string `json:"table"`
	ForEach    string `json:"for_each"`
	ForEachRow bool   `json:"for_each_row"`
	Condition  string `json:"condition"`
	Function   string `json:"function"`
}

// View describes a view with a raw SQL Definition and optional Materialized storage.
// For ClickHouse materialized views, Definition includes TO/ENGINE and AS SELECT;
// ordinary views contain SELECT, optionally prefixed by DEFINER/SQL SECURITY and AS.
type View struct {
	Name         string `json:"name"`
	Definition   string `json:"definition"`
	Materialized bool   `json:"materialized"`
}
