package postgres

const (
	// Int2 is a signed two-byte integer.
	Int2 = "smallint"
	// Int4 is a signed four-byte integer.
	Int4 = "integer"
	// Int8 is a signed eight-byte integer.
	Int8 = "bigint"
	// Float4 is a single-precision floating-point number.
	Float4 = "real"
	// Float8 is a double-precision floating-point number.
	Float8 = "double precision"
	// Numeric is an exact decimal with unconstrained precision and scale.
	Numeric = "numeric"
	// Numeric102 is a decimal with precision 10 and scale 2.
	Numeric102 = "numeric(10,2)"
	// Boolean stores true or false.
	Boolean = "boolean"
	// Text is a variable-length string without a length limit.
	Text = "text"
	// VarChar is a variable-length string with an optional length constraint.
	VarChar = "character varying"
	// VarChar255 is a string limited to 255 characters.
	VarChar255 = "character varying(255)"
	// Bytea stores binary data.
	Bytea = "bytea"
	// UUID stores a universally unique identifier.
	UUID = "uuid"
	// JSON stores the original text of a JSON value.
	JSON = "json"
	// JSONB stores JSON in a decomposed binary format.
	JSONB = "jsonb"
	// Date stores a calendar date.
	Date = "date"
	// Time stores a time of day without time zone.
	Time = "time without time zone"
	// Timestamp is a timestamp without time zone.
	Timestamp = "timestamp without time zone"
	// TimestampTZ is a timestamp with time zone.
	TimestampTZ = "timestamp with time zone"
)

// Nextval returns a Column.Default expression for an existing table_column_seq
// sequence. Table and column are unqualified names; the result uses search_path.
func Nextval(table, column string) string {
	return "nextval(" + QuoteLiteral(QuoteIdent(table+"_"+column+"_seq")) + "::regclass)"
}
