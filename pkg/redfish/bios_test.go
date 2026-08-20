package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func biosRouter(t *testing.T) (*mux.Router, *handler) {
	t.Helper()

	h := NewHandler(testUsername, testPassword, nil)
	router := mux.NewRouter()
	registerBiosSettingsRoutes(router, passThroughAuth, h)

	return router, h
}

// Systems/{id}/Bios answered 501, which stops machine setup dead: the platform
// configuration step reads BIOS attributes before it can decide anything.
func TestBios_ServesAttributes(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := encodeAsServed(t, h.GetBios("1"))

	assert.Equal(t, "/redfish/v1/Systems/1/Bios", body["@odata.id"])
	assert.Equal(t, "BIOS", body["Id"])
	assert.Equal(t, "BIOS Configuration", body["Name"])

	// libredfish's Bios model requires @odata.context: real BMCs always send
	// it, so it is not optional, and its absence fails the whole
	// deserialization rather than losing one field.
	assert.Equal(t, "/redfish/v1/$metadata#Bios.Bios", body["@odata.context"])

	attributes, ok := body["Attributes"].(map[string]any)
	require.True(t, ok, "Attributes must be an object, got %T", body["Attributes"])

	// The set a Dell host is checked against. A missing attribute produces no
	// diff and a present-and-wrong one does, so these are served with the
	// expected values to keep the setup report clean.
	for key, want := range map[string]string{
		"ConTermType":                  "Vt100Vt220",
		"FailSafeBaud":                 "115200",
		"HttpDev1EnDis":                "Enabled",
		"HttpDev1TlsMode":              "None",
		"InBandManageabilityInterface": "Disabled",
		"PxeDev1EnDis":                 "Disabled",
		"RedirAfterBoot":               "Enabled",
		"SerialComm":                   "OnConRedirAuto",
		"SerialPortAddress":            "Serial1Com2Serial2Com1",
		"SriovGlobalEnable":            "Enabled",
		"Tpm2Algorithm":                "SHA256",
		"Tpm2Hierarchy":                "Enabled",
		"TpmSecurity":                  "On",
		"UefiVariableAccess":           "Standard",
	} {
		assert.Equal(t, want, attributes[key], "attribute %s", key)
	}

	// Named devices that do not exist here are deliberately absent: absent
	// produces no diff, present-and-wrong does.
	assert.NotContains(t, attributes, "SetBootOrderEn")
	assert.NotContains(t, attributes, "HttpDev1Interface")
}

// The settings object is how a client discovers where to stage writes. Without
// the annotation it has to guess the URI.
func TestBios_AdvertisesSettingsObject(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := encodeAsServed(t, h.GetBios("1"))

	settings, ok := body["@Redfish.Settings"].(map[string]any)
	require.True(t, ok, "@Redfish.Settings must be present, got %T", body["@Redfish.Settings"])

	object, ok := settings["SettingsObject"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "/redfish/v1/Systems/1/Bios/Settings", object["@odata.id"])
}

// Every advertised action must answer. An advertised action that fails reads as
// a broken BMC rather than as an unsupported feature, which is the same mistake
// as advertising a nav link that 501s.
func TestBios_AdvertisedActionsHaveTargets(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	actions, ok := encodeAsServed(t, h.GetBios("1"))["Actions"].(map[string]any)
	require.True(t, ok)

	for _, name := range []string{"#Bios.ResetBios", "#Bios.ChangePassword"} {
		action, present := actions[name].(map[string]any)
		require.True(t, present, "%s must be advertised", name)
		assert.NotEmpty(t, action["target"])
	}
}

// GET on the settings object must work before anything has been staged, rather
// than 404 until first write.
func TestBiosSettings_ServedWhenNothingStaged(t *testing.T) {
	router, _ := biosRouter(t)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/1/Bios/Settings", nil))

	require.Equal(t, http.StatusOK, recorder.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	assert.Equal(t, "/redfish/v1/Systems/1/Bios/Settings", body["@odata.id"])
	assert.Empty(t, body["Attributes"])
}

// The staging round trip: a PATCH is accepted, reads back from the settings
// object, and is reflected in the live attributes.
func TestBiosSettings_PatchStagesAndReadsBack(t *testing.T) {
	router, h := biosRouter(t)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch,
		"/redfish/v1/Systems/1/Bios/Settings",
		strings.NewReader(`{"Attributes":{"PxeDev1EnDis":"Enabled","BootMode":"Uefi"}}`)))

	require.Equal(t, http.StatusOK, recorder.Code)

	// Readable from the settings object.
	staged := httptest.NewRecorder()
	router.ServeHTTP(staged, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/1/Bios/Settings", nil))

	var settings struct {
		Attributes map[string]string `json:"Attributes"`
	}
	require.NoError(t, json.Unmarshal(staged.Body.Bytes(), &settings))
	assert.Equal(t, "Enabled", settings.Attributes["PxeDev1EnDis"])
	assert.Equal(t, "Uefi", settings.Attributes["BootMode"])

	// And reflected live. On real firmware the staged value would wait for a
	// reboot; here there is no BIOS and no boot to wait for, so withholding it
	// would look like a BMC that silently refused the write.
	live, ok := encodeAsServed(t, h.GetBios("1"))["Attributes"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Enabled", live["PxeDev1EnDis"])
	assert.Equal(t, "Uefi", live["BootMode"])

	// Untouched attributes survive: a PATCH is partial, not a replacement.
	assert.Equal(t, "Vt100Vt220", live["ConTermType"])
}

// A PATCH with an empty or absent Attributes object is a no-op, not an error.
func TestBiosSettings_EmptyPatchIsAccepted(t *testing.T) {
	router, _ := biosRouter(t)

	for _, body := range []string{`{}`, `{"Attributes":{}}`} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch,
			"/redfish/v1/Systems/1/Bios/Settings", strings.NewReader(body)))
		assert.Equal(t, http.StatusOK, recorder.Code, "body %s must be accepted", body)
	}
}

// Resetting restores the startup set and clears the staged one.
func TestBios_ResetRestoresDefaults(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	h.StageBiosAttributes(map[string]any{"PxeDev1EnDis": "Enabled"})
	require.Equal(t, "Enabled",
		encodeAsServed(t, h.GetBios("1"))["Attributes"].(map[string]any)["PxeDev1EnDis"])

	h.ResetBios()

	assert.Equal(t, "Disabled",
		encodeAsServed(t, h.GetBios("1"))["Attributes"].(map[string]any)["PxeDev1EnDis"])
	assert.Empty(t, h.bios.pending())
}

// A non-string attribute is dropped rather than coerced: it would otherwise
// read back as a type the client never wrote.
func TestBios_IgnoresNonStringAttributes(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	h.StageBiosAttributes(map[string]any{"PxeDev1EnDis": true, "ConTermType": "Vt100Vt220"})

	attributes := encodeAsServed(t, h.GetBios("1"))["Attributes"].(map[string]any)
	assert.Equal(t, "Disabled", attributes["PxeDev1EnDis"], "a bool must not overwrite a string attribute")
	assert.Equal(t, "Vt100Vt220", attributes["ConTermType"])
}
