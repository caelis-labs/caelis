// Package jsonvalue owns recursive cloning and validation for SDK values that
// cross public or durable map[string]any boundaries.
package jsonvalue

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
)

// ValidationError reports a value that cannot be represented as JSON without
// losing its object/array/scalar shape.
type ValidationError struct {
	Path   string
	Reason string
	Err    error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	path := strings.TrimSpace(e.Path)
	if path == "" {
		path = "$"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" && e.Err != nil {
		reason = e.Err.Error()
	}
	if reason == "" {
		reason = "invalid JSON-compatible value"
	}
	return fmt.Sprintf("%s: %s", path, reason)
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Validate verifies that value is a finite, acyclic JSON-compatible value.
// Object keys must be strings. Structs are allowed when encoding/json can
// encode them, which preserves typed SDK metadata such as sandbox constraints.
func Validate(value any) error {
	if err := validateReflect(reflect.ValueOf(value), "$", map[visit]bool{}); err != nil {
		return err
	}
	if _, err := json.Marshal(value); err != nil {
		return &ValidationError{Path: "$", Reason: "value is not JSON-serializable", Err: err}
	}
	return nil
}

// ValidateMap validates one JSON-compatible object.
func ValidateMap(value map[string]any) error {
	return Validate(value)
}

// Clone recursively copies maps, slices, pointers, arrays, interfaces, and
// exported struct fields. Valid JSON-compatible inputs never share mutable
// descendants with the returned value.
func Clone(value any) any {
	if value == nil {
		return nil
	}
	return cloneReflect(reflect.ValueOf(value), map[visit]reflect.Value{}).Interface()
}

// CloneMap recursively copies one map value.
func CloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	cloned, _ := Clone(value).(map[string]any)
	return cloned
}

type visit struct {
	typeOf reflect.Type
	kind   reflect.Kind
	ptr    uintptr
}

func validateReflect(value reflect.Value, path string, stack map[visit]bool) error {
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		key := visit{typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if stack[key] {
			return &ValidationError{Path: path, Reason: "cyclic value"}
		}
		stack[key] = true
		defer delete(stack, key)
		return validateReflect(value.Elem(), path, stack)
	}

	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return nil
	case reflect.Float32, reflect.Float64:
		if number := value.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			return &ValidationError{Path: path, Reason: "non-finite number"}
		}
		return nil
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return &ValidationError{Path: path, Reason: "object key type must be string"}
		}
		key := visit{typeOf: value.Type(), kind: value.Kind(), ptr: uintptr(value.UnsafePointer())}
		if stack[key] {
			return &ValidationError{Path: path, Reason: "cyclic value"}
		}
		stack[key] = true
		defer delete(stack, key)
		iterator := value.MapRange()
		for iterator.Next() {
			name := iterator.Key().String()
			if err := validateReflect(iterator.Value(), objectPath(path, name), stack); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		key := visit{typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if key.ptr != 0 {
			if stack[key] {
				return &ValidationError{Path: path, Reason: "cyclic value"}
			}
			stack[key] = true
			defer delete(stack, key)
		}
		for i := 0; i < value.Len(); i++ {
			if err := validateReflect(value.Index(i), fmt.Sprintf("%s[%d]", path, i), stack); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateReflect(value.Index(i), fmt.Sprintf("%s[%d]", path, i), stack); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		typeOf := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typeOf.Field(i)
			if field.PkgPath != "" || strings.Split(field.Tag.Get("json"), ",")[0] == "-" {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			if err := validateReflect(value.Field(i), objectPath(path, name), stack); err != nil {
				return err
			}
		}
		return nil
	default:
		return &ValidationError{Path: path, Reason: fmt.Sprintf("unsupported %s value", value.Kind())}
	}
}

func cloneReflect(value reflect.Value, memo map[visit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneReflect(value.Elem(), memo)
		out := reflect.New(value.Type()).Elem()
		if cloned.IsValid() && cloned.Type().AssignableTo(value.Type()) {
			out.Set(cloned)
		} else if cloned.IsValid() && cloned.Type().Implements(value.Type()) {
			out.Set(cloned)
		}
		return out
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		key := visit{typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if cached, ok := memo[key]; ok {
			return cached
		}
		out := reflect.New(value.Type().Elem())
		memo[key] = out
		out.Elem().Set(cloneReflect(value.Elem(), memo))
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		key := visit{typeOf: value.Type(), kind: value.Kind(), ptr: uintptr(value.UnsafePointer())}
		if cached, ok := memo[key]; ok {
			return cached
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		memo[key] = out
		iterator := value.MapRange()
		for iterator.Next() {
			out.SetMapIndex(cloneReflect(iterator.Key(), memo), cloneReflect(iterator.Value(), memo))
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		key := visit{typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer()}
		if key.ptr != 0 {
			if cached, ok := memo[key]; ok {
				return cached
			}
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		if key.ptr != 0 {
			memo[key] = out
		}
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneReflect(value.Index(i), memo))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneReflect(value.Index(i), memo))
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := 0; i < value.NumField(); i++ {
			if out.Field(i).CanSet() && value.Field(i).CanInterface() {
				out.Field(i).Set(cloneReflect(value.Field(i), memo))
			}
		}
		return out
	default:
		return value
	}
}

func objectPath(parent string, key string) string {
	if key == "" {
		return parent + "[\"\"]"
	}
	return parent + "." + key
}
