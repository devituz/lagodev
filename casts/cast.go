// Package casts provides a small attribute-casting layer the ORM applies on
// reads and writes. Each cast maps SQL ↔ Go through a registered Cast.
// Built-in casts (and their aliases): json (jsonb), bool (boolean),
// int (integer), float (double), date, datetime.
//
// Custom casts can be registered with Register() at init time.
package casts

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cast converts a value coming back from the DB (FromDB) and going to the DB
// (ToDB). Implementations should be allocation-conscious; they may be called
// many times per row.
type Cast interface {
	FromDB(src any, dst any) error
	ToDB(src any) (any, error)
}

var (
	mu       sync.RWMutex
	registry = map[string]Cast{}
)

// Register installs a cast under the given name.
func Register(name string, c Cast) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = c
}

// Get returns the cast with the given name, or nil.
func Get(name string) Cast {
	mu.RLock()
	defer mu.RUnlock()
	return registry[name]
}

// JSONCast serializes/deserializes any value via encoding/json.
type JSONCast struct{}

// FromDB unmarshals src ([]byte / string) into dst (pointer).
func (JSONCast) FromDB(src any, dst any) error {
	if src == nil {
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("casts: json: unsupported source type %T", src)
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, dst)
}

// ToDB marshals src to JSON bytes.
func (JSONCast) ToDB(src any) (any, error) {
	if src == nil {
		return nil, nil
	}
	return json.Marshal(src)
}

// BoolCast normalizes 0/1/"t"/"f"/"true"/"false" to bool.
type BoolCast struct{}

// FromDB writes the boolean interpretation of src into dst (*bool).
func (BoolCast) FromDB(src any, dst any) error {
	b, ok := dst.(*bool)
	if !ok {
		return fmt.Errorf("casts: bool: dst must be *bool")
	}
	switch v := src.(type) {
	case nil:
		*b = false
		return nil
	case bool:
		*b = v
		return nil
	case []byte:
		*b = truthyString(string(v))
		return nil
	case string:
		*b = truthyString(v)
		return nil
	}
	// Numeric kinds: any non-zero value is true.
	rv := reflect.ValueOf(src)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		*b = rv.Int() != 0
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		*b = rv.Uint() != 0
		return nil
	case reflect.Float32, reflect.Float64:
		*b = rv.Float() != 0
		return nil
	}
	return fmt.Errorf("casts: bool: unsupported source type %T", src)
}

// truthyString reports whether s is one of the recognized truthy tokens,
// mirroring config.Bool: 1/true/t/yes/y/on (case-insensitive, trimmed).
func truthyString(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	}
	return false
}

// ToDB returns the raw bool — drivers normalize it for the dialect.
func (BoolCast) ToDB(src any) (any, error) { return src, nil }

// IntCast parses string/numeric into int64.
type IntCast struct{}

// FromDB parses src into *int64. It accepts every signed/unsigned integer
// kind, integral floats (e.g. SQLite returns float64 for aggregates), and
// numeric strings — mirroring database/sql's convertAssign behaviour.
func (IntCast) FromDB(src any, dst any) error {
	out, ok := dst.(*int64)
	if !ok {
		return fmt.Errorf("casts: int: dst must be *int64")
	}
	switch v := src.(type) {
	case nil:
		*out = 0
		return nil
	case []byte:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return err
		}
		*out = n
		return nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return err
		}
		*out = n
		return nil
	}
	rv := reflect.ValueOf(src)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		*out = rv.Int()
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return fmt.Errorf("casts: int: value %d overflows int64", u)
		}
		*out = int64(u)
		return nil
	case reflect.Float32, reflect.Float64:
		fv := rv.Float()
		if fv != math.Trunc(fv) {
			return fmt.Errorf("casts: int: non-integral float %v", fv)
		}
		*out = int64(fv)
		return nil
	}
	return fmt.Errorf("casts: int: unsupported source type %T", src)
}

// ToDB returns src as-is.
func (IntCast) ToDB(src any) (any, error) { return src, nil }

// FloatCast parses string/numeric into float64.
type FloatCast struct{}

// FromDB parses src into *float64, accepting any numeric kind, []byte and
// string sources.
func (FloatCast) FromDB(src any, dst any) error {
	out, ok := dst.(*float64)
	if !ok {
		return fmt.Errorf("casts: float: dst must be *float64")
	}
	switch v := src.(type) {
	case nil:
		*out = 0
		return nil
	case []byte:
		n, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return err
		}
		*out = n
		return nil
	case string:
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		*out = n
		return nil
	}
	rv := reflect.ValueOf(src)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		*out = rv.Float()
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		*out = float64(rv.Int())
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		*out = float64(rv.Uint())
		return nil
	}
	return fmt.Errorf("casts: float: unsupported source type %T", src)
}

// ToDB returns src as-is.
func (FloatCast) ToDB(src any) (any, error) { return src, nil }

// DateCast renders/parses values as YYYY-MM-DD.
type DateCast struct{}

// FromDB parses src into *time.Time.
func (DateCast) FromDB(src any, dst any) error {
	out, ok := dst.(*time.Time)
	if !ok {
		return fmt.Errorf("casts: date: dst must be *time.Time")
	}
	switch v := src.(type) {
	case nil:
		return nil
	case time.Time:
		*out = v
	case []byte:
		t, err := time.Parse("2006-01-02", string(v))
		if err != nil {
			return err
		}
		*out = t
	case string:
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return err
		}
		*out = t
	default:
		return fmt.Errorf("casts: date: unsupported source type %T", src)
	}
	return nil
}

// ToDB renders time.Time as a date string. A nil source, a nil *time.Time, or
// the zero time render as NULL.
func (DateCast) ToDB(src any) (any, error) {
	t, ok := timeFromSrc(src)
	if !ok {
		return nil, fmt.Errorf("casts: date: src must be time.Time")
	}
	if t == nil || t.IsZero() {
		return nil, nil
	}
	return t.Format("2006-01-02"), nil
}

// DateTimeCast renders/parses values as RFC3339 timestamps.
type DateTimeCast struct{}

// FromDB parses src into *time.Time, accepting RFC3339 and the common
// "2006-01-02 15:04:05" SQL datetime layout.
func (DateTimeCast) FromDB(src any, dst any) error {
	out, ok := dst.(*time.Time)
	if !ok {
		return fmt.Errorf("casts: datetime: dst must be *time.Time")
	}
	var s string
	switch v := src.(type) {
	case nil:
		return nil
	case time.Time:
		*out = v
		return nil
	case []byte:
		s = string(v)
	case string:
		s = v
	default:
		return fmt.Errorf("casts: datetime: unsupported source type %T", src)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			*out = t
			return nil
		}
	}
	return fmt.Errorf("casts: datetime: cannot parse %q", s)
}

// ToDB renders time.Time as an RFC3339 string. A nil source, a nil *time.Time,
// or the zero time render as NULL.
func (DateTimeCast) ToDB(src any) (any, error) {
	t, ok := timeFromSrc(src)
	if !ok {
		return nil, fmt.Errorf("casts: datetime: src must be time.Time")
	}
	if t == nil || t.IsZero() {
		return nil, nil
	}
	return t.Format(time.RFC3339), nil
}

// timeFromSrc normalizes a time-ish source into a *time.Time. It returns
// (nil, true) for a nil interface or nil *time.Time so callers can emit NULL.
func timeFromSrc(src any) (*time.Time, bool) {
	switch v := src.(type) {
	case nil:
		return nil, true
	case time.Time:
		return &v, true
	case *time.Time:
		return v, true
	}
	return nil, false
}

func init() {
	Register("json", JSONCast{})
	Register("jsonb", JSONCast{})
	Register("bool", BoolCast{})
	Register("boolean", BoolCast{})
	Register("int", IntCast{})
	Register("integer", IntCast{})
	Register("float", FloatCast{})
	Register("double", FloatCast{})
	Register("date", DateCast{})
	Register("datetime", DateTimeCast{})
}
