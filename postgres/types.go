package postgres

import "strings"

const (
	// Int8 is a signed eight-byte integer.
	Int8 = "bigint"
	// Text is a variable-length string without a length limit.
	Text = "text"
	// VarChar255 is a string limited to 255 characters.
	VarChar255 = "character varying(255)"
	// Numeric102 is a decimal with precision 10 and scale 2.
	Numeric102 = "numeric(10,2)"
	// TimestampTZ is a timestamp with time zone.
	TimestampTZ = "timestamp with time zone"
)

// Nextval returns a default expression for the conventional table_column_seq
// sequence. Table and column are unqualified names; the result uses search_path.
func Nextval(table, column string) string {
	name := `"` + strings.ReplaceAll(table+"_"+column+"_seq", `"`, `""`) + `"`
	return "nextval('" + strings.ReplaceAll(name, "'", "''") + "'::regclass)"
}
