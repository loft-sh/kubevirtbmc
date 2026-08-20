package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// The BIOS resource and its settings object.
//
// Systems/{id}/Bios answered 501, which stops machine setup dead: the platform
// configuration step reads BIOS attributes before it can decide what to change.
// Settings is the standard Redfish staging pattern -- writes go to the settings
// object and take effect on the next boot, rather than being applied live --
// and it has no generated route, so it is registered here.
const (
	biosPathTemplate         = "/redfish/v1/Systems/%s/Bios"
	biosSettingsPathTemplate = "/redfish/v1/Systems/%s/Bios/Settings"

	// The reference mock reports v1_2_0 rather than the v1_2_2 of the model in
	// this tree. Matching the reference is the safer choice for a string a
	// client may match on.
	biosOdataType = "#Bios.v1_2_0.Bios"

	// libredfish's Bios model requires @odata.context: real BMCs always send
	// it, so it is not optional in the struct, and without it the whole
	// response fails to deserialize rather than merely losing a field. Same
	// family as the DelliDRACCard trap.
	biosOdataContext = "/redfish/v1/$metadata#Bios.Bios"

	biosID   = "BIOS"
	biosName = "BIOS Configuration"
)

// expectedDellBiosAttributes is the set a Dell host is checked against.
//
// A MISSING attribute produces no diff; only one that is present and wrong
// does. So serving these is not strictly required, and serving them with the
// expected values is strictly better than serving them wrong: it means the
// setup check reports a clean host instead of a list of differences nobody can
// act on, because there is no real BIOS here to reconfigure.
//
// The values are the primary spellings, not the legacy ones some checks also
// accept: SerialComm is OnConRedirAuto rather than OnConRedir, and
// SerialPortAddress is Serial1Com2Serial2Com1 rather than Com1.
func expectedDellBiosAttributes() map[string]string {
	return map[string]string{
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

		// Not in the checked set, but Dell's infinite-boot check reads it, and
		// these hosts must keep retrying network boot until the provisioning
		// agent answers. Disabled would stop after one failed attempt.
		"BootSeqRetry": "Enabled",

		// SetBootOrderEn is deliberately NOT served: it names a specific Dell
		// boot sequence that does not exist here, and a present-and-wrong value
		// produces a diff where an absent one produces nothing.
		//
		// HttpDev1Interface is NOT here either, but for the opposite reason: it
		// is required, and it is filled in per request from the actual NIC id
		// rather than baked in as a constant. See GetBios.
	}
}

// biosState holds the live attributes and the pending ones.
//
// Staged writes are kept separate from the live values, which is what the
// settings pattern means: a PATCH to Settings changes what the next boot would
// apply, not what the BIOS currently reports. Keeping them apart lets a client
// read back what it staged without the live view lying about the current state.
type biosState struct {
	mu sync.RWMutex

	attributes map[string]string
	staged     map[string]string
}

func newBiosState() *biosState {
	return &biosState{
		attributes: expectedDellBiosAttributes(),
		staged:     map[string]string{},
	}
}

func (b *biosState) live() map[string]string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return copyStringMap(b.attributes)
}

func (b *biosState) pending() map[string]string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return copyStringMap(b.staged)
}

// stage records a PATCH against the settings object.
//
// The staged values are also applied to the live attributes. On real firmware
// they would not be, until the host reboots and the BIOS picks them up. Here
// there is no BIOS and no boot to wait for, so holding them back would mean a
// client stages a change, reboots, reads the attributes and finds its change
// missing -- which reads as a BIOS that silently refused the write.
func (b *biosState) stage(attributes map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for key, value := range attributes {
		b.staged[key] = value
		b.attributes[key] = value
	}
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}

	return out
}

func asAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}

	return out
}

// GetBios serves the live BIOS resource.
//
// Built by hand rather than from the generated model so that @odata.context and
// the @Redfish.Settings annotation can both be included: the model has no field
// for either, and the first is mandatory to the client while the second is how
// a client discovers where to stage writes.
func (h *handler) GetBios(computerSystemID string) map[string]any {
	return map[string]any{
		"@odata.context":    biosOdataContext,
		"@odata.id":         fmt.Sprintf(biosPathTemplate, computerSystemID),
		"@odata.type":       biosOdataType,
		"Id":                biosID,
		"Name":              biosName,
		"Description":       "BIOS Configuration Current Settings",
		"AttributeRegistry": "BiosAttributeRegistry.v1_0_0",
		"Attributes":        h.biosAttributes(),

		// Where to PATCH. Without this a client has to guess the settings URI.
		"@Redfish.Settings": map[string]any{
			"@odata.type": "#Settings.v1_3_5.Settings",
			"SettingsObject": map[string]any{
				"@odata.id": fmt.Sprintf(biosSettingsPathTemplate, computerSystemID),
			},
		},

		"Actions": map[string]any{
			"#Bios.ResetBios": map[string]any{
				"target": fmt.Sprintf(biosPathTemplate, computerSystemID) + "/Actions/Bios.ResetBios",
			},
			"#Bios.ChangePassword": map[string]any{
				"target": fmt.Sprintf(biosPathTemplate, computerSystemID) + "/Actions/Bios.ChangePassword",
			},
		},
	}
}

// getBiosSettings serves the settings object: the same resource shape, holding
// what has been staged rather than what is live.
func (h *handler) getBiosSettings(computerSystemID string) map[string]any {
	return map[string]any{
		"@odata.context": biosOdataContext,
		"@odata.id":      fmt.Sprintf(biosSettingsPathTemplate, computerSystemID),
		"@odata.type":    biosOdataType,
		"Id":             "Settings",
		"Name":           "BIOS Configuration Pending Settings",
		"Description":    "BIOS Configuration Pending Settings",
		"Attributes":     asAnyMap(h.bios.pending()),
	}
}

// StageBiosAttributes applies a PATCH of BIOS attributes.
//
// Accepted from either the settings object or the BIOS resource itself. The
// settings object is the correct target and the one a well-behaved client uses,
// but accepting both costs nothing and a PATCH refused here stalls machine
// setup.
func (h *handler) StageBiosAttributes(attributes map[string]any) {
	staged := map[string]string{}
	for key, raw := range attributes {
		value, isString := raw.(string)
		if !isString {
			// BIOS attributes are strings on the wire for every attribute this
			// serves. Anything else would read back as a type the client did
			// not write, so it is dropped rather than coerced.
			logrus.WithField("attribute", key).
				Warn("Ignoring non-string BIOS attribute in PATCH")
			continue
		}
		staged[key] = value
	}

	if len(staged) == 0 {
		return
	}

	h.bios.stage(staged)

	logrus.WithField("count", len(staged)).Info("Staged BIOS attributes")
}

// registerBiosSettingsRoutes wires the settings object, which has no generated
// route of its own.
func registerBiosSettingsRoutes(
	router *mux.Router,
	authMiddleware mux.MiddlewareFunc,
	h *handler,
) {
	settings := router.NewRoute().Subrouter()
	settings.Use(authMiddleware)

	path := fmt.Sprintf(biosSettingsPathTemplate, "{computerSystemId}")

	settings.Methods(http.MethodGet).Path(path).HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, h.getBiosSettings(mux.Vars(r)["computerSystemId"]))
		})

	for _, method := range []string{http.MethodPatch, http.MethodPut} {
		settings.Methods(method).Path(path).HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Attributes map[string]any `json:"Attributes"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{
						"error": map[string]any{
							"code":    "Base.1.0.GeneralError",
							"message": fmt.Sprintf("malformed request body: %v", err),
						},
					})
					return
				}

				h.StageBiosAttributes(body.Attributes)

				writeJSON(w, http.StatusOK, h.getBiosSettings(mux.Vars(r)["computerSystemId"]))
			})
	}
}

// ResetBios restores the attributes served at startup and clears anything
// staged. That is what resetting a BIOS to its defaults means, and here the
// startup set IS the default.
func (h *handler) ResetBios() {
	h.bios.mu.Lock()
	defer h.bios.mu.Unlock()

	h.bios.attributes = expectedDellBiosAttributes()
	h.bios.staged = map[string]string{}

	logrus.Info("Reset BIOS attributes to defaults")
}

// biosAttributes is the live attribute set with the NIC-derived entries filled
// in.
//
// HttpDev1Interface names the interface HTTP boot device 1 goes out of. It has
// to be present: the Dell attribute comparison returns a hard error on the
// FIRST key it cannot find rather than skipping it, so one absent key stalls
// the whole poll indefinitely -- and a stalled phase is actively harmful,
// because the watchdog power-cycles the host and re-runs setup on a timer.
//
// It is derived from the interface rather than hard-coded, and a value staged
// by a client wins, so it can still be corrected over the wire.
func (h *handler) biosAttributes() map[string]any {
	attributes := h.bios.live()

	if _, staged := attributes["HttpDev1Interface"]; !staged {
		attributes["HttpDev1Interface"] = h.bootNICID()
	}

	return asAnyMap(attributes)
}
