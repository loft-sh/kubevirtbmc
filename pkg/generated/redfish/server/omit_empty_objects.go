// This file is hand-written. It is not produced by the OpenAPI generator, and
// `make generate` will not recreate it.

package server

import (
	"bytes"
	"encoding/json"
)

// omitEmptyObjects round-trips v through JSON and returns an equivalent value
// with every property whose value is an empty JSON object dropped. The caller
// encodes the result in place of v.
//
// The models in this package are generated with their complex-typed fields as
// values rather than as pointers, so `omitempty` can never fire on them:
// encoding/json only considers the empty string, 0, false, nil, and empty
// maps/slices to be empty, never a zero-valued struct. An unset navigation
// link such as ServiceRoot.Fabrics therefore goes out as `"Fabrics":{}`.
//
// Redfish types those properties as references carrying a mandatory
// `@odata.id`, so a strict client rejects the whole document instead of reading
// `{}` as "unsupported". NVIDIA NICo's site-explorer is one such client:
//
//	Failed to deserialize data from https://.../redfish/v1/:
//	missing field `@odata.id` at line 1 column 260
//
// where column 260 was `"AggregationService":{}`. Per Redfish, an *absent*
// property is how a service says it does not implement something, which is
// what this produces. Inventing an `@odata.id` instead would only move the
// failure to a 404.
//
// Only empty objects are dropped. Empty arrays, empty strings, and zero
// numbers are left alone: an empty collection legitimately serves
// `"Members":[]` alongside `"Members@odata.count":0`, and both have to survive.
//
// Pruning is bottom-up: a parent left empty because every one of its
// properties was dropped is dropped in turn. An empty object is never a useful
// answer in Redfish. It is fatal where a reference is expected, and where a
// complex type is expected it is at best noise and at worst the same trap:
// System.SerialConsole is rejected unless it carries its SSH and IPMI
// sub-objects, and ProtocolFeaturesSupported must be absent rather than empty
// so that clients do not enable $expand against a service that cannot serve
// it. Omitting the property says "not supported", which is both true and what
// Redfish means.
func omitEmptyObjects(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	// Keep numeric literals byte-identical across the round trip; without this
	// every number becomes a float64 and int64 counts can come back in
	// exponent form.
	decoder.UseNumber()

	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return nil, err
	}

	object, ok := tree.(map[string]any)
	if !ok {
		// Not a JSON object -- an error string, for instance. Nothing to prune,
		// so hand back the original value untouched.
		return v, nil
	}

	dropEmptyObjects(object)

	return object, nil
}

// dropEmptyObjects removes the empty-object properties of object, recursing
// first so that a property left empty by its own pruning is removed too.
func dropEmptyObjects(object map[string]any) {
	for name, value := range object {
		switch typed := value.(type) {
		case map[string]any:
			dropEmptyObjects(typed)
			if len(typed) == 0 {
				delete(object, name)
			}
		case []any:
			dropEmptyObjectsInArray(typed)
		}
	}
}

// dropEmptyObjectsInArray recurses into array elements. Elements are never
// removed: dropping one would renumber the rest and contradict the
// `Members@odata.count` that ships with it.
func dropEmptyObjectsInArray(array []any) {
	for _, element := range array {
		switch typed := element.(type) {
		case map[string]any:
			dropEmptyObjects(typed)
		case []any:
			dropEmptyObjectsInArray(typed)
		}
	}
}
