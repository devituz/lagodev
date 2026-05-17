// Package casts provides a small attribute-casting layer the ORM applies on
// reads and writes. Each cast maps SQL ↔ Go through a registered Cast.
// Built-in casts: json, bool, int, float, date, datetime.
//
// Custom casts can be registered with Register() at init time.
package casts

import (
	"encoding/json"
	"fmt"
	"strconv"
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
	case bool:
		*b = v
	case int64:
		*b = v != 0
	case []byte:
		*b = string(v) == "1" || string(v) == "true" || string(v) == "t"
	case string:
		*b = v == "1" || v == "true" || v == "t"
	default:
		return fmt.Errorf("casts: bool: unsupported source type %T", src)
	}
	return nil
}

// ToDB returns the raw bool — drivers normalize it for the dialect.
func (BoolCast) ToDB(src any) (any, error) { return src, nil }

// IntCast parses string/numeric into int64.
type IntCast struct{}

// FromDB parses src into *int64.
func (IntCast) FromDB(src any, dst any) error {
	out, ok := dst.(*int64)
	if !ok {
		return fmt.Errorf("casts: int: dst must be *int64")
	}
	switch v := src.(type) {
	case int64:
		*out = v
	case int:
		*out = int64(v)
	case []byte:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return err
		}
		*out = n
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return err
		}
		*out = n
	default:
		return fmt.Errorf("casts: int: unsupported source type %T", src)
	}
	return nil
}

// ToDB returns src as-is.
func (IntCast) ToDB(src any) (any, error) { return src, nil }

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

// ToDB renders time.Time as a date string.
func (DateCast) ToDB(src any) (any, error) {
	t, ok := src.(time.Time)
	if !ok {
		return nil, fmt.Errorf("casts: date: src must be time.Time")
	}
	return t.Format("2006-01-02"), nil
}

func init() {
	Register("json", JSONCast{})
	Register("jsonb", JSONCast{})
	Register("bool", BoolCast{})
	Register("boolean", BoolCast{})
	Register("int", IntCast{})
	Register("integer", IntCast{})
	Register("date", DateCast{})
}
