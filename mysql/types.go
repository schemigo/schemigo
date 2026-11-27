package mysql

// Common column types for MySQL schema declarations.
const (
	BigInt    = "bigint"
	Int       = "int"
	SmallInt  = "smallint"
	TinyInt   = "tinyint"
	Boolean   = "boolean"
	Text      = "text"
	Date      = "date"
	DateTime  = "datetime"
	Timestamp = "timestamp"
	JSON      = "json"

	// MediumInt is a signed three-byte integer.
	MediumInt = "mediumint"
	// Float is a single-precision floating-point number.
	Float = "float"
	// Double is a double-precision floating-point number.
	Double = "double"
	// Decimal102 is a decimal with precision 10 and scale 2.
	Decimal102 = "decimal(10,2)"
	// VarChar255 is a string limited to 255 characters.
	VarChar255 = "varchar(255)"
	// Blob stores variable-length binary data.
	Blob = "blob"
	// Binary16 stores exactly 16 bytes.
	Binary16 = "binary(16)"
	// Time stores a time of day or an elapsed time.
	Time = "time"
)
