package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/schemigo/schemigo"
)

type viewColumn struct {
	Name     string
	Type     uint32
	Modifier int32
}
type routineDependent struct {
	Kind        string `json:"kind"`
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Table       string `json:"table"`
	Description string `json:"description"`
}
type viewMetadata interface {
	compatible(schemigo.View) (bool, error)
	dependents(kind, name, identity string) ([]routineDependent, error)
}
type viewChanges struct {
	drops, creates []schemigo.Statement
	rebuilt        map[string]bool
	triggerDrops   map[string]bool
}

func planViews(current, desired schemigo.Schema, functions []functionChange, meta viewMetadata) (viewChanges, error) {
	result := viewChanges{rebuilt: map[string]bool{}, triggerDrops: map[string]bool{}}
	old, wanted := map[string]schemigo.View{}, map[string]schemigo.View{}
	create := map[string]bool{}
	for _, v := range current.Views {
		if err := postgresName(v.Name); err != nil {
			return result, err
		}
		if v.Name == "schema_migrations" {
			return result, fmt.Errorf("schema_migrations is reserved")
		}
		old[v.Name] = v
	}
	for _, v := range desired.Views {
		if v.Name == "schema_migrations" {
			return result, fmt.Errorf("schema_migrations is reserved")
		}
		if err := postgresName(v.Name); err != nil {
			return result, err
		}
		if strings.TrimSpace(v.Definition) == "" || strings.ContainsRune(v.Definition, 0) {
			return result, fmt.Errorf("view %q has an empty or invalid definition", v.Name)
		}
		wanted[v.Name] = v
	}
	visiting := map[string]bool{}
	var drop func(string) error
	var dependents func(string, string, string) error
	dependents = func(kind, name, identity string) error {
		if meta == nil {
			return fmt.Errorf("%s %q requires a catalog connection to inspect dependencies", kind, name)
		}
		deps, err := meta.dependents(kind, name, identity)
		if err != nil {
			return err
		}
		slices.SortFunc(deps, func(a, b routineDependent) int {
			return strings.Compare(a.Kind+a.Schema+a.Table+a.Name, b.Kind+b.Schema+b.Table+b.Name)
		})
		for _, d := range deps {
			if d.Schema == current.Name && d.Kind == "view" {
				if _, ok := old[d.Name]; ok {
					if err := drop(d.Name); err != nil {
						return err
					}
					continue
				}
			}
			if d.Schema == current.Name && d.Kind == "trigger" {
				key := d.Table + "\x00" + d.Name
				if slices.ContainsFunc(current.Triggers, func(t schemigo.Trigger) bool { return triggerKey(t) == key }) {
					result.triggerDrops[key] = true
					continue
				}
			}
			return fmt.Errorf("%s %q has unmanaged dependency %s (%s.%s)", kind, name, d.Description, d.Schema, d.Name)
		}
		return nil
	}
	drop = func(name string) error {
		if result.rebuilt[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("cyclic view dependency at %q", name)
		}
		visiting[name] = true
		if err := dependents("view", name, ""); err != nil {
			return err
		}
		visiting[name] = false
		result.rebuilt[name] = true
		v := old[name]
		kind := "VIEW"
		if v.Materialized {
			kind = "MATERIALIZED VIEW"
		}
		result.drops = append(result.drops, tableDDL("DROP "+kind+" IF EXISTS "+Qualified(current.Name, name), v.Materialized))
		if _, ok := wanted[name]; ok {
			create[name] = true
		}
		return nil
	}
	for _, v := range current.Views {
		next, exists := wanted[v.Name]
		if exists && v.Materialized == next.Materialized && sqlDefinitionKey(v.Definition) == sqlDefinitionKey(next.Definition) {
			continue
		}
		if !exists || v.Materialized || v.Materialized != next.Materialized {
			if err := drop(v.Name); err != nil {
				return result, err
			}
		} else {
			if meta == nil {
				return result, fmt.Errorf("view %q requires a catalog connection to compare columns", v.Name)
			}
			compatible, err := meta.compatible(next)
			if err != nil {
				return result, err
			}
			if !compatible {
				if err := drop(v.Name); err != nil {
					return result, err
				}
			}
			create[v.Name] = true
		}
	}
	for _, f := range functions {
		if !functionNeedsDrop(f) {
			continue
		}
		// A standalone DROP without a connection still uses RESTRICT at execution.
		if meta == nil && f.next == nil {
			continue
		}
		if err := dependents("function", f.old.Name, f.identity); err != nil {
			return result, err
		}
	}
	if len(current.Views) > 0 || meta != nil {
		for _, name := range changedViewTables(current, desired) {
			if err := dependents("table", name, ""); err != nil {
				return result, err
			}
		}
	}
	for _, v := range desired.Views {
		if _, ok := old[v.Name]; !ok {
			create[v.Name] = true
		}
	}
	ordered, err := orderViews(desired)
	if err != nil {
		return result, err
	}
	for _, v := range ordered {
		if create[v.Name] {
			result.creates = append(result.creates, createView(desired.Name, v))
		}
	}
	return result, nil
}

// The PostgreSQL catalog supplies dependencies for existing objects. For new
// definitions this conservative token scan orders references to managed views.
// It may reject a false cycle when a column/CTE shares a managed view name.
func orderViews(s schemigo.Schema) ([]schemigo.View, error) {
	views := map[string]schemigo.View{}
	for _, v := range s.Views {
		views[v.Name] = v
	}
	state := map[string]int{}
	var result []schemigo.View
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 2 {
			return nil
		}
		if state[name] == 1 {
			return fmt.Errorf("cyclic or ambiguous view reference at %q", name)
		}
		state[name] = 1
		refs := map[string]bool{}
		tokens := expressionTokens(views[name].Definition)
		for i, t := range tokens {
			id := strings.ToLower(t)
			if strings.HasPrefix(t, "\"") {
				id = strings.ReplaceAll(strings.Trim(t, "\""), "\"\"", "\"")
			}
			if _, ok := views[id]; !ok {
				continue
			}
			if i > 0 && tokens[i-1] == "." {
				if i < 2 || tokens[i-2] != QuoteIdent(s.Name) && tokens[i-2] != s.Name {
					continue
				}
			}
			refs[id] = true
		}
		names := []string{}
		for n := range refs {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			if err := visit(n); err != nil {
				return err
			}
		}
		state[name] = 2
		result = append(result, views[name])
		return nil
	}
	names := []string{}
	for n := range views {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func createView(schema string, v schemigo.View) schemigo.Statement {
	prefix := "CREATE OR REPLACE VIEW "
	if v.Materialized {
		prefix = "CREATE MATERIALIZED VIEW IF NOT EXISTS "
	}
	definition := strings.TrimSuffix(strings.TrimSpace(v.Definition), ";")
	return tableSchemaScope(schema, tableDDL(prefix+Qualified(schema, v.Name)+" AS\n"+definition+"\n", false))
}

type postgresViewMetadata struct {
	ctx    context.Context
	conn   *sql.Conn
	tx     *sql.Tx
	schema string
}

func changedViewTables(current, desired schemigo.Schema) []string {
	var names []string
	for _, old := range current.Tables {
		var next *schemigo.Table
		for i := range desired.Tables {
			if desired.Tables[i].Name == old.Name {
				next = &desired.Tables[i]
				break
			}
		}
		changed := next == nil
		if next != nil {
			for _, c := range old.Columns {
				found := false
				for _, n := range next.Columns {
					if c.Name == n.Name {
						found = columnTypeKey(current, c.Type) == columnTypeKey(desired, n.Type)
						break
					}
				}
				changed = changed || !found
			}
		}
		if changed {
			names = append(names, old.Name)
		}
	}
	slices.Sort(names)
	return names
}

func diffViews(ctx context.Context, conn *sql.Conn, current, desired schemigo.Schema, functions []functionChange) (result viewChanges, err error) {
	needsCatalog := false
	if len(current.Views) > 0 || conn != nil {
		needsCatalog = len(changedViewTables(current, desired)) > 0
	}
	for _, v := range current.Views {
		match := slices.ContainsFunc(desired.Views, func(n schemigo.View) bool {
			return v.Name == n.Name && v.Materialized == n.Materialized && sqlDefinitionKey(v.Definition) == sqlDefinitionKey(n.Definition)
		})
		needsCatalog = needsCatalog || !match
	}
	for _, f := range functions {
		needsCatalog = needsCatalog || functionNeedsDrop(f) && (conn != nil || f.next != nil)
	}
	if !needsCatalog || conn == nil {
		return planViews(current, desired, functions, nil)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return result, fmt.Errorf("view diff snapshot: %w", err)
	}
	defer func() {
		e := tx.Rollback()
		if e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, e)
		}
	}()
	var path string
	if err = tx.QueryRowContext(ctx, "SELECT pg_catalog.set_config('search_path', 'pg_catalog, ' || pg_catalog.quote_ident($1), true)", current.Name).Scan(&path); err != nil {
		return result, err
	}
	result, err = planViews(current, desired, functions, &postgresViewMetadata{ctx: ctx, conn: conn, tx: tx, schema: current.Name})
	return result, err
}

func (m *postgresViewMetadata) compatible(v schemigo.View) (bool, error) {
	var old []viewColumn
	rows, err := m.tx.QueryContext(m.ctx, viewColumnsQuery, Qualified(m.schema, v.Name))
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var c viewColumn
		if err = rows.Scan(&c.Name, &c.Type, &c.Modifier); err != nil {
			rows.Close()
			return false, err
		}
		old = append(old, c)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return false, errors.Join(err, closeErr)
	}
	// Describe via the extended protocol: it resolves output types/typmods without
	// executing SELECT, including volatile expressions in the requested view.
	if _, err = m.tx.ExecContext(m.ctx, "SAVEPOINT schemigo_view_description"); err != nil {
		return false, err
	}
	var next []viewColumn
	err = m.conn.Raw(func(raw any) error {
		c, ok := raw.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("view column description requires a pgx connection")
		}
		query := "SELECT * FROM (\n" + strings.TrimSuffix(strings.TrimSpace(v.Definition), ";") + "\n) AS schemigo_view LIMIT 0"
		description, e := c.Conn().PgConn().Prepare(m.ctx, "", query, nil)
		if e != nil {
			return e
		}
		for _, f := range description.Fields {
			next = append(next, viewColumn{Name: f.Name, Type: f.DataTypeOID, Modifier: f.TypeModifier})
		}
		return nil
	})
	if err != nil {
		if _, rollbackErr := m.tx.ExecContext(m.ctx, "ROLLBACK TO SAVEPOINT schemigo_view_description"); rollbackErr != nil {
			return false, errors.Join(err, rollbackErr)
		}
		// Missing objects/columns can be created by the earlier table phase.
		var code interface{ SQLState() string }
		if !errors.As(err, &code) || code.SQLState() != "42P01" && code.SQLState() != "42703" && code.SQLState() != "42883" {
			return false, fmt.Errorf("describe view %q: %w", v.Name, err)
		}
	}
	if _, e := m.tx.ExecContext(m.ctx, "RELEASE SAVEPOINT schemigo_view_description"); e != nil {
		return false, e
	}
	if err != nil || len(old) > len(next) {
		return false, nil
	}
	for i, c := range old {
		if c != next[i] {
			return false, nil
		}
	}
	return true, nil
}

func (m *postgresViewMetadata) dependents(kind, name, identity string) ([]routineDependent, error) {
	object := Qualified(m.schema, name)
	if kind == "function" {
		object += "(" + identity + ")"
	}
	query := viewDependentsQuery
	if kind == "table" {
		query = tableViewDependentsQuery
	}
	if kind == "function" {
		query = functionDependentsQuery
	}
	rows, err := m.tx.QueryContext(m.ctx, query, object)
	if err != nil {
		return nil, fmt.Errorf("dependencies of %s: %w", object, err)
	}
	defer rows.Close()
	var result []routineDependent
	for rows.Next() {
		var raw []byte
		var d routineDependent
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

const viewColumnsQuery = `
SELECT attname, atttypid, atttypmod FROM pg_catalog.pg_attribute
WHERE attrelid=pg_catalog.to_regclass($1) AND attnum>0 AND NOT attisdropped ORDER BY attnum`

// Auto/internal dependencies such as a view's row type and own _RETURN rule
// disappear with the view. Other dependencies must be represented in the model.
const routineDependentsSelect = `
SELECT json_build_object(
 'kind',CASE WHEN r.oid IS NOT NULL AND r.rulename='_RETURN' AND v.relkind IN ('v','m') THEN 'view'
             WHEN t.oid IS NOT NULL AND NOT t.tgisinternal AND t.tgconstraint=0 THEN 'trigger' ELSE 'unknown' END,
 'schema',COALESCE(vn.nspname,tn.nspname,''),
 'name',COALESCE(v.relname,t.tgname,''),
 'table',COALESCE(tr.relname,''),
 'description',pg_catalog.pg_describe_object(d.classid,d.objid,d.objsubid))
FROM pg_catalog.pg_depend d
LEFT JOIN pg_catalog.pg_rewrite r ON d.classid='pg_catalog.pg_rewrite'::regclass AND r.oid=d.objid
LEFT JOIN pg_catalog.pg_class v ON v.oid=r.ev_class
LEFT JOIN pg_catalog.pg_namespace vn ON vn.oid=v.relnamespace
LEFT JOIN pg_catalog.pg_trigger t ON d.classid='pg_catalog.pg_trigger'::regclass AND t.oid=d.objid
LEFT JOIN pg_catalog.pg_class tr ON tr.oid=t.tgrelid
LEFT JOIN pg_catalog.pg_namespace tn ON tn.oid=tr.relnamespace
`
const viewDependentsQuery = routineDependentsSelect + `
WHERE d.refclassid='pg_catalog.pg_class'::regclass AND d.refobjid=pg_catalog.to_regclass($1)
AND NOT (d.classid='pg_catalog.pg_rewrite'::regclass AND r.ev_class=d.refobjid AND r.rulename='_RETURN')
AND NOT (d.classid='pg_catalog.pg_type'::regclass AND d.deptype='i')
UNION ALL
SELECT json_build_object('kind','unknown','description','view metadata (owner, grants, comment or storage options)')
FROM pg_catalog.pg_class c WHERE c.oid=pg_catalog.to_regclass($1)
AND (c.relowner<>(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user) OR c.relacl IS NOT NULL
 OR pg_catalog.obj_description(c.oid,'pg_class') IS NOT NULL OR c.reloptions IS NOT NULL OR c.reltablespace<>0
 OR EXISTS(SELECT FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND (a.attacl IS NOT NULL OR pg_catalog.col_description(c.oid,a.attnum) IS NOT NULL)))
`

const tableViewDependentsQuery = routineDependentsSelect + `
WHERE d.refclassid='pg_catalog.pg_class'::regclass AND d.refobjid=pg_catalog.to_regclass($1)
AND d.classid='pg_catalog.pg_rewrite'::regclass`
const functionDependentsQuery = routineDependentsSelect + `
WHERE d.refclassid='pg_catalog.pg_proc'::regclass AND d.refobjid=pg_catalog.to_regprocedure($1)
UNION ALL
SELECT json_build_object('kind','unknown','description','function metadata (owner, grants or comment)')
FROM pg_catalog.pg_proc p WHERE p.oid=pg_catalog.to_regprocedure($1)
AND (p.proowner<>(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user) OR p.proacl IS NOT NULL
 OR pg_catalog.obj_description(p.oid,'pg_proc') IS NOT NULL)
`
