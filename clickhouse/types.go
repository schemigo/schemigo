package clickhouse

// Common column types for ClickHouse schema declarations.
const (
	UInt8    = "UInt8"
	UInt16   = "UInt16"
	UInt32   = "UInt32"
	UInt64   = "UInt64"
	Int8     = "Int8"
	Int16    = "Int16"
	Int32    = "Int32"
	Int64    = "Int64"
	Float32  = "Float32"
	Float64  = "Float64"
	String   = "String"
	Date     = "Date"
	DateTime = "DateTime"
	UUID     = "UUID"
	Bool     = "Bool"

	// Date32 stores dates over a wider range than Date.
	Date32 = "Date32"
	// DateTime64Millis stores timestamps with millisecond precision.
	DateTime64Millis = "DateTime64(3)"
	// JSON stores JSON documents.
	JSON = "JSON"
	// IPv4 stores an IPv4 address.
	IPv4 = "IPv4"
	// IPv6 stores an IPv6 address.
	IPv6 = "IPv6"
)
