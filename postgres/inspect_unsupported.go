package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// Refuse metadata the current model would discard rather than promise a
// round trip that could later destroy an object's behavior.
func rejectUnsupported(ctx context.Context, tx *sql.Tx, name string) error {
	for _, query := range []string{unsupportedRelationsQuery, unsupportedFunctionsQuery, unsupportedTriggersQuery} {
		rows, err := tx.QueryContext(ctx, query, name)
		if err != nil {
			return fmt.Errorf("inspect postgres capabilities: %w", err)
		}
		if rows.Next() {
			var object, reason string
			scanErr := rows.Scan(&object, &reason)
			closeErr := rows.Close()
			if scanErr != nil {
				return fmt.Errorf("inspect postgres capabilities: %w", scanErr)
			}
			if closeErr != nil {
				return fmt.Errorf("inspect postgres capabilities: %w", closeErr)
			}
			return fmt.Errorf("inspect postgres %q: %s: %s is not supported by the schema model", name, object, reason)
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil {
			return fmt.Errorf("inspect postgres capabilities: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("inspect postgres capabilities: %w", closeErr)
		}
	}
	return nil
}

const unsupportedRelationsQuery = managedRelations + `
SELECT object, reason FROM (
 SELECT c.relname::text AS object, 'partitioning, inheritance, foreign table, storage options or row security' AS reason
 FROM relations c WHERE c.relkind IN ('p', 'f') OR c.relispartition
 OR c.relrowsecurity OR c.relforcerowsecurity OR c.reloptions IS NOT NULL
 OR c.reltablespace <> 0 OR c.relpersistence <> 'p'
 OR EXISTS (SELECT FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid OR i.inhparent = c.oid)
 UNION ALL
 SELECT c.relname || '.' || a.attname, 'identity, generated column or custom collation'
 FROM relations c JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid
 JOIN pg_catalog.pg_type typ ON typ.oid = a.atttypid
 WHERE a.attnum > 0 AND NOT a.attisdropped
 AND (a.attidentity <> '' OR a.attgenerated <> '' OR a.attcollation <> typ.typcollation)
 UNION ALL
 SELECT c.relname || '.' || ic.relname, 'index INCLUDE, NULLS NOT DISTINCT, invalid state, storage options or view index'
 FROM relations c JOIN pg_catalog.pg_index i ON i.indrelid = c.oid
 JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
 WHERE i.indnatts <> i.indnkeyatts OR i.indnullsnotdistinct
 OR NOT i.indisvalid OR NOT i.indisready OR i.indisclustered OR NOT i.indislive
 OR ic.reloptions IS NOT NULL OR ic.reltablespace <> 0 OR c.relkind <> 'r'
 UNION ALL
 SELECT c.relname || '.' || con.conname, 'constraint options or UNIQUE/EXCLUDE constraint'
 FROM relations c JOIN pg_catalog.pg_constraint con ON con.conrelid = c.oid
 WHERE con.contype IN ('u', 'x') OR (con.contype IN ('p', 'f', 'c') AND
 (con.condeferrable OR con.condeferred OR NOT con.convalidated))
 OR (con.contype = 'c' AND con.connoinherit)
 OR (con.contype = 'f' AND (con.confmatchtype <> 's' OR con.confdelsetcols IS NOT NULL OR
  (SELECT r.relnamespace FROM pg_catalog.pg_class r WHERE r.oid = con.confrelid) <> c.relnamespace))
) unsupported ORDER BY object LIMIT 1`

const unsupportedFunctionsQuery = managedFunctions + `
SELECT p.proname::text, 'routine kind, language, SQL-standard body or function options'
FROM functions p JOIN pg_catalog.pg_language l ON l.oid = p.prolang
WHERE p.prokind <> 'f' OR l.lanname NOT IN ('sql', 'plpgsql')
 OR p.prosqlbody IS NOT NULL OR p.prosecdef OR p.proleakproof OR p.proisstrict
 OR p.provolatile <> 'v' OR p.proparallel <> 'u' OR p.proconfig IS NOT NULL
ORDER BY p.proname LIMIT 1`

const unsupportedTriggersQuery = managedRelations + `
SELECT c.relname || '.' || t.tgname, 'trigger arguments, column events, transition tables, mode or external function'
FROM relations c JOIN pg_catalog.pg_trigger t ON t.tgrelid = c.oid
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
WHERE NOT t.tgisinternal AND t.tgconstraint = 0
 AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend d
  WHERE d.classid = 'pg_catalog.pg_trigger'::regclass AND d.objid = t.oid AND d.deptype = 'e')
 AND (t.tgnargs <> 0 OR cardinality(t.tgattr::smallint[]) <> 0
  OR t.tgoldtable IS NOT NULL OR t.tgnewtable IS NOT NULL OR t.tgenabled <> 'O'
  OR p.pronamespace <> c.relnamespace OR c.relkind NOT IN ('r', 'v'))
ORDER BY c.relname, t.tgname LIMIT 1`
