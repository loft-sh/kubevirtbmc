package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"strings"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// The Dell attributes resource is not reachable by navigation: no link to it
// exists on the Manager. Clients construct the URI from the Manager's own
// @odata.id with the manager id repeated as the last segment, so the path is a
// hard interface and cannot be renamed.
func TestDellAttributes_ServedAtTheConstructedURI(t *testing.T) {
	router := mux.NewRouter()
	registerDellOemRoutes(router, passThroughAuth, resourcemanager.DefaultManagerId)

	for _, path := range []string{
		"/redfish/v1/Managers/BMC/Oem/Dell/DellAttributes/BMC",
		// libredfish probes the standard location first in several flows and
		// falls back to the OEM path on a 404, so both are served.
		"/redfish/v1/Managers/BMC/Attributes",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			require.Equal(t, http.StatusOK, recorder.Code)

			var body struct {
				OdataID    string         `json:"@odata.id"`
				OdataType  string         `json:"@odata.type"`
				ID         string         `json:"Id"`
				Name       string         `json:"Name"`
				Attributes map[string]any `json:"Attributes"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

			// The canonical id is the OEM path even when reached by the
			// standard one, so a client that follows it lands somewhere real.
			assert.Equal(t, "/redfish/v1/Managers/BMC/Oem/Dell/DellAttributes/BMC", body.OdataID)
			assert.Equal(t, "#DellAttributes.v1_0_0.DellAttributes", body.OdataType)
			// Id is deliberately not the last segment of the URI.
			assert.Equal(t, "iDRACAttributes", body.ID)
			assert.Equal(t, "OEMAttributeRegistry", body.Name)

			// These two are the load-bearing pair: together they resolve the
			// internal lockdown status instead of hard-erroring exploration.
			// Both are read with str_value(), which returns None for any
			// non-string JSON type, and the result is unwrapped -- so a number
			// or a bool here fails exactly like absence.
			for key, want := range map[string]string{
				"Lockdown.1.SystemLockdown": "Disabled",
				"Racadm.1.Enable":           "Enabled",
			} {
				value, ok := body.Attributes[key]
				require.True(t, ok, "%s must be present", key)
				str, isString := value.(string)
				require.True(t, isString, "%s must be a JSON string, got %T", key, value)
				assert.Equal(t, want, str)
			}
		})
	}
}

// Every attribute must serialize as a string, not just the two that are read
// today: the reader yields None for any other JSON type, so a bool or a number
// is indistinguishable from a missing attribute.
func TestDellAttributes_AllValuesAreStrings(t *testing.T) {
	router := mux.NewRouter()
	registerDellOemRoutes(router, passThroughAuth, resourcemanager.DefaultManagerId)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/redfish/v1/Managers/BMC/Oem/Dell/DellAttributes/BMC", nil))

	var body struct {
		Attributes map[string]any `json:"Attributes"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	require.Len(t, body.Attributes, 10)
	for key, value := range body.Attributes {
		_, isString := value.(string)
		assert.True(t, isString, "%s must be a JSON string, got %T", key, value)
	}
}

// A PATCH merges the keys it carries rather than replacing the map, which is
// what a Redfish attribute PATCH means. Clients do write here.
func TestDellAttributes_PatchMergesAndKeepsTheRest(t *testing.T) {
	router := mux.NewRouter()
	registerDellOemRoutes(router, passThroughAuth, resourcemanager.DefaultManagerId)

	patch := `{"Attributes":{"Lockdown.1.SystemLockdown":"Enabled"}}`
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch,
		"/redfish/v1/Managers/BMC/Oem/Dell/DellAttributes/BMC", stringReader(patch)))
	require.Equal(t, http.StatusOK, recorder.Code)

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/redfish/v1/Managers/BMC/Oem/Dell/DellAttributes/BMC", nil))

	var body struct {
		Attributes map[string]string `json:"Attributes"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	assert.Equal(t, "Enabled", body.Attributes["Lockdown.1.SystemLockdown"], "patched key must change")
	assert.Equal(t, "Enabled", body.Attributes["Racadm.1.Enable"], "untouched key must survive")
	assert.Len(t, body.Attributes, 10)
}

// The Manager's Oem.Dell.DelliDRACCard block is mandatory once Oem.Dell
// exists, and every field of it is a bare String rather than an Option in the
// client's type. A half-populated block deserializes worse than an absent one:
// it fails the BMC time-sync check, which is the entry point to the
// power-off / BMC-reset remediation sequence.
func TestManager_CarriesFullDelliDRACCard(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	manager := resourcemanager.NewManager(
		resourcemanager.DefaultManagerId, "Manager", "22222222-2222-2222-2222-222222222222")

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetManager().Return(manager, nil)

	h := NewHandler(testUsername, testPassword, mockRM)
	served, err := h.GetManager()
	require.NoError(t, err)

	body := encodeAsServed(t, served)

	oem, ok := body["Oem"].(map[string]any)
	require.True(t, ok, "Oem must be present, got %T", body["Oem"])
	dell, ok := oem["Dell"].(map[string]any)
	require.True(t, ok, "Oem.Dell must be present: its absence hard-errors exploration")
	card, ok := dell["DelliDRACCard"].(map[string]any)
	require.True(t, ok, "DelliDRACCard must be present, got %T", dell["DelliDRACCard"])

	// All ten, none optional in the client's struct, none allowed to be empty.
	for _, key := range []string{
		"@odata.context", "@odata.id", "@odata.type",
		"Description", "IPMIVersion", "Id",
		"LastSystemInventoryTime", "LastUpdateTime", "Name", "URLString",
	} {
		value, present := card[key]
		require.True(t, present, "DelliDRACCard.%s is required", key)
		str, isString := value.(string)
		require.True(t, isString, "DelliDRACCard.%s must be a string, got %T", key, value)
		// Non-empty matters for the pruner as much as for the client: an empty
		// object would be dropped, and a hollowed block fails to deserialize.
		assert.NotEmpty(t, str, "DelliDRACCard.%s must not be empty", key)
	}

	assert.Equal(t, "DelliDRACCard", card["Name"])
	assert.Equal(t, "2.0", card["IPMIVersion"])
	assert.Equal(t, "BMC-1_0x23_IDRACinfo", card["Id"])
}

// The OEM surface is served only when the BMC claims to be a Dell, so the
// vendor it reports and the OEM extensions it exposes cannot drift apart.
func TestIdentity_IsDellIsCaseInsensitive(t *testing.T) {
	for vendor, want := range map[string]bool{
		"Dell": true, "dell": true, "DELL": true,
		"Supermicro": false, "": false,
	} {
		assert.Equal(t, want, serviceRootIdentity{vendor: vendor}.isDell(), "vendor %q", vendor)
	}
}

func passThroughAuth(next http.Handler) http.Handler { return next }

func stringReader(s string) *strings.Reader { return strings.NewReader(s) }
