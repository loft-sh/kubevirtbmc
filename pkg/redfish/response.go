package redfish

import (
	"encoding/json"
	"reflect"
	"strings"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
)

// odataRefType is server.OdataV4IdRef, a Redfish reference carrying only an
// @odata.id. References the handler does not populate are left as the zero
// OdataV4IdRef. Go's encoding/json ignores `omitempty` on struct-valued fields,
// so such a reference serializes as `{}` without the required @odata.id, which
// strict Redfish clients reject when deserializing. sanitize re-encodes a
// response body and omits every zero-valued OdataV4IdRef wherever it occurs.
var odataRefType = reflect.TypeOf(server.OdataV4IdRef{})

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

func sanitize(body any) json.RawMessage {
	cleaned, _ := cleanValue(reflect.ValueOf(body))
	data, err := json.Marshal(cleaned)
	if err != nil {
		// Unreachable: cleanValue yields only maps, slices, and scalars.
		data, _ = json.Marshal(body)
	}
	return data
}

// cleanValue mirrors encoding/json's output for v but drops zero-valued
// OdataV4IdRef references. The bool reports whether the value must be omitted
// entirely (an unset reference); callers skip the field, slice element, or map
// entry it came from.
func cleanValue(v reflect.Value) (any, bool) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, false
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == odataRefType {
			return v.Interface(), v.Field(0).String() == ""
		}

		// Types with their own JSON encoding (e.g. time.Time) must be emitted
		// via MarshalJSON, not reflected into field by field — time.Time's
		// fields are unexported, so reflection would yield `{}` instead of the
		// RFC 3339 string strict clients expect.
		if v.Type().Implements(jsonMarshalerType) {
			return v.Interface(), false
		}

		out := map[string]any{}
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			name, omitempty := jsonField(f)
			if name == "-" {
				continue
			}
			fv := v.Field(i)
			if omitempty && isEmptyValue(fv) {
				continue
			}
			cv, omit := cleanValue(fv)
			if omit {
				continue
			}
			out[name] = cv
		}
		return out, false

	case reflect.Slice:
		if v.IsNil() {
			return nil, false
		}
		fallthrough
	case reflect.Array:
		arr := make([]any, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			cv, omit := cleanValue(v.Index(i))
			if omit {
				continue
			}
			arr = append(arr, cv)
		}
		return arr, false

	case reflect.Map:
		if v.IsNil() {
			return nil, false
		}
		m := map[string]any{}
		for _, k := range v.MapKeys() {
			cv, omit := cleanValue(v.MapIndex(k))
			if omit {
				continue
			}
			m[k.String()] = cv
		}
		return m, false

	default:
		return v.Interface(), false
	}
}

func jsonField(f reflect.StructField) (name string, omitempty bool) {
	parts := strings.Split(f.Tag.Get("json"), ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	case reflect.Struct:
		// Go's encoding/json ignores `omitempty` on struct-valued fields, so an
		// unset nested object (e.g. a Manager's CommandShell/GraphicalConsole)
		// serializes as `{}` and fails strict clients that require its fields.
		// Treat an all-zero struct as empty so omitempty drops it as intended.
		return v.IsZero()
	}
	return false
}
