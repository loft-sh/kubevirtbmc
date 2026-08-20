// This file is hand-written. It is not produced by the OpenAPI generator.

package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func encode(t *testing.T, v any) string {
	t.Helper()

	recorder := httptest.NewRecorder()
	if err := EncodeJSONResponse(v, nil, recorder); err != nil {
		t.Fatalf("EncodeJSONResponse: %v", err)
	}

	return recorder.Body.String()
}

// A zero-valued reference field cannot be omitted by encoding/json, so it has
// to be dropped after the fact.
func TestEncodeJSONResponse_DropsEmptyReferences(t *testing.T) {
	body := encode(t, struct {
		Set   OdataV4IdRef `json:"Set,omitempty"`
		Unset OdataV4IdRef `json:"Unset,omitempty"`
	}{
		Set: OdataV4IdRef{OdataId: "/redfish/v1/Systems"},
	})

	want := `{"Set":{"@odata.id":"/redfish/v1/Systems"}}` + "\n"
	if body != want {
		t.Fatalf("got %s want %s", body, want)
	}
}

// Empty arrays, empty strings and zero numbers are not empty objects and must
// survive untouched: an empty collection answers with `Members: []` and a
// `Members@odata.count` of 0, and both halves carry meaning.
func TestEncodeJSONResponse_KeepsEmptyNonObjects(t *testing.T) {
	body := encode(t, struct {
		Members []OdataV4IdRef `json:"Members"`
		Count   int64          `json:"Members@odata.count"`
		Name    string         `json:"Name"`
	}{
		Members: []OdataV4IdRef{},
	})

	want := `{"Members":[],"Members@odata.count":0,"Name":""}` + "\n"
	if body != want {
		t.Fatalf("got %s want %s", body, want)
	}
}

// Large integers must not come back in exponent form. Round-tripping through
// `any` without UseNumber turns every number into a float64 and would serve
// 1e+07 where a client expects 10000000.
func TestEncodeJSONResponse_PreservesLargeIntegers(t *testing.T) {
	body := encode(t, struct {
		SpeedMbps int64 `json:"SpeedMbps"`
	}{
		SpeedMbps: 10000000,
	})

	want := `{"SpeedMbps":10000000}` + "\n"
	if body != want {
		t.Fatalf("got %s want %s", body, want)
	}
}

// Nested references are dropped at any depth, including inside collection
// members. Members themselves are never removed: dropping one would renumber
// the rest and contradict the count served alongside them.
func TestEncodeJSONResponse_DropsNestedAndKeepsMemberCount(t *testing.T) {
	type member struct {
		OdataId string       `json:"@odata.id"`
		Unset   OdataV4IdRef `json:"Unset,omitempty"`
	}

	body := encode(t, struct {
		Members []member `json:"Members"`
		Links   struct {
			Unset OdataV4IdRef `json:"Unset,omitempty"`
		} `json:"Links"`
	}{
		Members: []member{{OdataId: "/redfish/v1/Systems/1"}},
	})

	// Links empties out once its only property is dropped, so it goes too.
	// Pruning is bottom-up: an object that ends up empty is never a useful
	// answer, and for some properties an empty object is itself rejected.
	want := `{"Members":[{"@odata.id":"/redfish/v1/Systems/1"}]}` + "\n"
	if body != want {
		t.Fatalf("got %s want %s", body, want)
	}
}

// The error path encodes a bare string. A non-object payload has nothing to
// prune and must pass straight through.
func TestEncodeJSONResponse_PassesThroughNonObjects(t *testing.T) {
	if body := encode(t, "invalid request"); body != `"invalid request"`+"\n" {
		t.Fatalf("got %s", body)
	}

	if body := encode(t, []string{}); body != "[]\n" {
		t.Fatalf("got %s", body)
	}
}

// A nil body writes no content at all, as before.
func TestEncodeJSONResponse_NilWritesNothing(t *testing.T) {
	recorder := httptest.NewRecorder()
	if err := EncodeJSONResponse(nil, nil, recorder); err != nil {
		t.Fatalf("EncodeJSONResponse: %v", err)
	}
	if body := recorder.Body.String(); body != "" {
		t.Fatalf("got %q want empty", body)
	}
}

// omitEmptyObjects must not corrupt a payload it does not understand; anything
// it returns has to remain valid JSON, including when everything prunes away.
func TestOmitEmptyObjects_ReturnsValidJSON(t *testing.T) {
	pruned, err := omitEmptyObjects(map[string]any{
		"Empty":  map[string]any{},
		"Nested": map[string]any{"Deep": map[string]any{"Empty": map[string]any{}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(pruned)
	if err != nil {
		t.Fatal(err)
	}

	// Collapses all the way up: Deep empties, so Nested empties, so both go.
	want := `{}`
	if string(raw) != want {
		t.Fatalf("got %s want %s", raw, want)
	}
}
