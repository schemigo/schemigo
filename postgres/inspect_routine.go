package postgres

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

// The WHEN extraction follows PostgreSQL's information_schema.triggers view;
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
 'condition', COALESCE(substring(pg_catalog.pg_get_triggerdef(t.oid)
  FROM ' WHEN \((.*)\) EXECUTE FUNCTION'), ''),
 'function', p.proname)
FROM relations c JOIN pg_catalog.pg_trigger t ON t.tgrelid = c.oid
JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
WHERE NOT t.tgisinternal AND t.tgconstraint = 0
AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend d
 WHERE d.classid = 'pg_catalog.pg_trigger'::regclass AND d.objid = t.oid AND d.deptype = 'e')
ORDER BY c.relname, t.tgname`
