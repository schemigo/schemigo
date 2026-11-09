package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/schemigo/schemigo"
)

type catalogTrigger struct {
	schemigo.Trigger
	Definition string `json:"definition"`
}

func inspectTriggers(ctx context.Context, tx *sql.Tx, name string) ([]schemigo.Trigger, error) {
	rows, err := inspectJSON[catalogTrigger](ctx, tx, name, triggersQuery)
	if err != nil {
		return nil, err
	}
	var triggers []schemigo.Trigger
	for _, row := range rows {
		if row.Definition != "" {
			row.Condition, err = triggerCondition(row.Definition)
			if err != nil {
				return nil, fmt.Errorf("trigger %q: %w", row.Name, err)
			}
		}
		triggers = append(triggers, row.Trigger)
	}
	return triggers, nil
}

// The first unquoted parentheses in a conditional pg_get_triggerdef enclose WHEN.
func triggerCondition(definition string) (string, error) {
	start, depth := -1, 0
	var quote byte
	var escape bool
	for i := 0; i < len(definition); i++ {
		c := definition[i]
		if quote != 0 {
			if escape && c == '\\' {
				i++
			} else if c == quote {
				if i+1 < len(definition) && definition[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			escape = c == '\'' && i > 0 && (definition[i-1] == 'E' || definition[i-1] == 'e')
		case '(':
			if start == -1 {
				start = i + 1
			}
			depth++
		case ')':
			depth--
			if start >= 0 && depth == 0 {
				return definition[start:i], nil
			}
		}
	}
	return "", fmt.Errorf("invalid trigger condition in definition")
}

const managedFunctions = `WITH functions AS (
 SELECT p.* FROM pg_catalog.pg_proc p
 JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
 WHERE n.nspname = $1
 AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend d
  WHERE d.classid = 'pg_catalog.pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
) `

const functionsQuery = managedFunctions + `
SELECT json_build_object('name', p.proname,
 'parameters', pg_catalog.pg_get_function_arguments(p.oid),
 'returns', pg_catalog.pg_get_function_result(p.oid),
 'language', l.lanname, 'body', p.prosrc)
FROM functions p JOIN pg_catalog.pg_language l ON l.oid = p.prolang
WHERE p.prokind = 'f'
ORDER BY p.proname, pg_catalog.pg_get_function_identity_arguments(p.oid)`

// pg_get_expr cannot deparse both OLD and NEW relation references.
const triggersQuery = managedRelations + `
SELECT json_build_object('name', t.tgname, 'table', c.relname,
 'when', CASE t.tgtype & 66 WHEN 2 THEN 'BEFORE' WHEN 64 THEN 'INSTEAD OF' ELSE 'AFTER' END,
 'event', concat_ws(' OR ',
  CASE WHEN t.tgtype & 4 <> 0 THEN 'INSERT' END,
  CASE WHEN t.tgtype & 8 <> 0 THEN 'DELETE' END,
  CASE WHEN t.tgtype & 16 <> 0 THEN 'UPDATE' END,
  CASE WHEN t.tgtype & 32 <> 0 THEN 'TRUNCATE' END),
 'for_each', CASE t.tgtype & 1 WHEN 1 THEN 'ROW' ELSE 'STATEMENT' END,
 'definition', CASE WHEN t.tgqual IS NULL THEN ''
  ELSE pg_catalog.pg_get_triggerdef(t.oid) END,
 'function', p.proname)
FROM relations c JOIN pg_catalog.pg_trigger t ON t.tgrelid = c.oid
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
WHERE NOT t.tgisinternal AND t.tgconstraint = 0
AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend d
 WHERE d.classid = 'pg_catalog.pg_trigger'::regclass AND d.objid = t.oid AND d.deptype = 'e')
ORDER BY c.relname, t.tgname`
