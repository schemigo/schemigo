package postgres

// Select managed relations once; all catalog names and schema filters are
// explicit so connection search_path cannot select a different namespace.
const managedRelations = `WITH relations AS (
 SELECT c.* FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = $1 AND c.relname <> 'schema_migrations'
 AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
 AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend d
  WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
) `

const tablesQuery = managedRelations + `
SELECT json_build_object(
 'name', c.relname,
 'columns', (SELECT json_agg(json_build_object(
   'name', a.attname, 'type', pg_catalog.format_type(a.atttypid, a.atttypmod),
   'not_null', a.attnotnull,
   'default', COALESCE(pg_catalog.pg_get_expr(ad.adbin, ad.adrelid), '')) ORDER BY a.attnum)
  FROM pg_catalog.pg_attribute a
  LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
  WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped),
 'primary_key', (SELECT json_agg(a.attname ORDER BY k.ord)
  FROM pg_catalog.pg_constraint con
  CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY k(num, ord)
  JOIN pg_catalog.pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.num
  WHERE con.conrelid = c.oid AND con.contype = 'p'),
 'indexes', (SELECT json_agg(json_build_object(
   'name', ic.relname, 'unique', i.indisunique, 'method', am.amname,
   'where', COALESCE(pg_catalog.pg_get_expr(i.indpred, i.indrelid), ''),
   'columns', CASE WHEN keys.sql = (
     SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY k.ord)
     FROM unnest(i.indkey) WITH ORDINALITY k(num, ord)
     LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.num)
    THEN (SELECT json_agg(a.attname ORDER BY k.ord)
     FROM unnest(i.indkey) WITH ORDINALITY k(num, ord)
     JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.num) END,
   'expression', CASE WHEN keys.sql = (
     SELECT string_agg(quote_ident(a.attname), ', ' ORDER BY k.ord)
     FROM unnest(i.indkey) WITH ORDINALITY k(num, ord)
     LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.num)
    THEN '' ELSE keys.sql END) ORDER BY ic.relname)
  FROM pg_catalog.pg_index i
  JOIN pg_catalog.pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_catalog.pg_am am ON am.oid = ic.relam
  CROSS JOIN LATERAL (SELECT pg_catalog.pg_get_indexdef(i.indexrelid) AS sql,
   format('CREATE %sINDEX %I ON %I.%I USING %I (',
    CASE WHEN i.indisunique THEN 'UNIQUE ' ELSE '' END,
    ic.relname, $1::text, c.relname, am.amname) AS prefix) definition
  CROSS JOIN LATERAL (SELECT substring(definition.sql FROM length(definition.prefix) + 1
   FOR length(definition.sql) - length(definition.prefix) - 1 -
    CASE WHEN i.indpred IS NULL THEN 0
     ELSE length(pg_catalog.pg_get_expr(i.indpred, i.indrelid)) + 7 END) AS sql) keys
  WHERE i.indrelid = c.oid AND NOT i.indisprimary),
 'foreign_keys', (SELECT json_agg(json_build_object(
   'name', con.conname,
   'columns', (SELECT json_agg(a.attname ORDER BY k.ord)
    FROM unnest(con.conkey) WITH ORDINALITY k(num, ord)
    JOIN pg_catalog.pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.num),
   'referenced_table', ref.relname,
   'referenced_columns', (SELECT json_agg(a.attname ORDER BY k.ord)
    FROM unnest(con.confkey) WITH ORDINALITY k(num, ord)
    JOIN pg_catalog.pg_attribute a ON a.attrelid = ref.oid AND a.attnum = k.num),
   'on_delete', CASE con.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
    WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END,
   'on_update', CASE con.confupdtype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
    WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END) ORDER BY con.conname)
  FROM pg_catalog.pg_constraint con
  JOIN pg_catalog.pg_class ref ON ref.oid = con.confrelid
  WHERE con.conrelid = c.oid AND con.contype = 'f'),
 'checks', (SELECT json_agg(json_build_object('name', con.conname,
   'expression', pg_catalog.pg_get_expr(con.conbin, con.conrelid)) ORDER BY con.conname)
  FROM pg_catalog.pg_constraint con WHERE con.conrelid = c.oid AND con.contype = 'c'))
FROM relations c WHERE c.relkind = 'r'
ORDER BY c.relname`
