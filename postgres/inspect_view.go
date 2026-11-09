package postgres

const viewsQuery = managedRelations + `
SELECT json_build_object('name', c.relname,
 'definition', regexp_replace(pg_catalog.pg_get_viewdef(c.oid, false), ';[[:space:]]*$', ''),
 'materialized', c.relkind = 'm')
FROM relations c WHERE c.relkind IN ('v', 'm')
ORDER BY c.relname`
