package reflectutil

import (
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// AssignScanned writes a value scanned from the DB (as an any holding the
// driver's concrete type, or nil for NULL) into the destination field. NULL
// coalesces to the field's Go zero value. Fields implementing sql.Scanner
// (sql.NullString, decimal/uuid types, ...) receive the raw value through
// their Scan method; everything else goes through ConvertAssign.
func AssignScanned(fv reflect.Value, raw any) error {
	if fv.CanAddr() {
		if sc, ok := fv.Addr().Interface().(sql.Scanner); ok {
			return sc.Scan(raw)
		}
	}
	if raw == nil {
		// NULL → zero value. For pointer fields leave nil; otherwise reset.
		fv.Set(reflect.Zero(fv.Type()))
		return nil
	}
	if fv.Kind() == reflect.Ptr {
		if sc, ok := reflect.New(fv.Type().Elem()).Interface().(sql.Scanner); ok {
			if err := sc.Scan(raw); err != nil {
				return err
			}
			fv.Set(reflect.ValueOf(sc))
			return nil
		}
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return ConvertAssign(fv.Interface(), raw)
	}
	return ConvertAssign(fv.Addr().Interface(), raw)
}

// ConvertAssign converts src (a driver value: int64, float64, bool, []byte,
// string or time.Time) into the pointer dest. It mirrors the subset of
// database/sql's convertAssign that the ORM relies on, with a reflection
// fallback for numeric/string kinds so non-default field types (int, uint,
// float32, named string types, ...) all work. Textual numbers and booleans
// ([]byte on MySQL's text protocol) are parsed.
func ConvertAssign(dest, src any) error {
	dv := reflect.ValueOf(dest).Elem()

	// Fast path: src is directly assignable to the destination type.
	sv := reflect.ValueOf(src)
	if sv.Type().AssignableTo(dv.Type()) {
		dv.Set(sv)
		return nil
	}

	switch dv.Kind() {
	case reflect.String:
		switch s := src.(type) {
		case string:
			dv.SetString(s)
			return nil
		case []byte:
			dv.SetString(string(s))
			return nil
		case int64:
			// reflect's int→string conversion would yield a rune ("\x07"),
			// so format numbers explicitly.
			dv.SetString(strconv.FormatInt(s, 10))
			return nil
		case float64:
			dv.SetString(strconv.FormatFloat(s, 'g', -1, 64))
			return nil
		case bool:
			dv.SetString(strconv.FormatBool(s))
			return nil
		case time.Time:
			dv.SetString(s.Format(time.RFC3339Nano))
			return nil
		}
		return fmt.Errorf("orm: cannot assign %T to %s", src, dv.Type())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, ok := ToInt64(src); ok {
			dv.SetInt(n)
			return nil
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if n, ok := ToInt64(src); ok {
			dv.SetUint(uint64(n))
			return nil
		}
		if s, ok := asText(src); ok {
			if u, err := strconv.ParseUint(s, 10, 64); err == nil {
				dv.SetUint(u)
				return nil
			}
		}
	case reflect.Float32, reflect.Float64:
		switch f := src.(type) {
		case float64:
			dv.SetFloat(f)
			return nil
		case int64:
			dv.SetFloat(float64(f))
			return nil
		}
		if s, ok := asText(src); ok {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				dv.SetFloat(f)
				return nil
			}
		}
	case reflect.Bool:
		switch v := src.(type) {
		case bool:
			dv.SetBool(v)
			return nil
		case int64:
			dv.SetBool(v != 0)
			return nil
		}
		if s, ok := asText(src); ok {
			if b, err := strconv.ParseBool(s); err == nil {
				dv.SetBool(b)
				return nil
			}
		}
	}

	// Convertible numeric kinds (e.g. int64 → named int, float64 → float32).
	if sv.Type().ConvertibleTo(dv.Type()) {
		dv.Set(sv.Convert(dv.Type()))
		return nil
	}
	return fmt.Errorf("orm: cannot assign %T to %s", src, dv.Type())
}

func asText(src any) (string, bool) {
	switch s := src.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	}
	return "", false
}

// ToInt64 converts a driver value (int64, int, float64, []byte, string) to int64.
func ToInt64(src any) (int64, bool) {
	switch n := src.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case []byte:
		if v, err := strconv.ParseInt(string(n), 10, 64); err == nil {
			return v, true
		}
	case string:
		if v, err := strconv.ParseInt(n, 10, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}
