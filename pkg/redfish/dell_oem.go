package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// Dell's iDRAC attributes are not reachable by navigation: the Manager carries
// no link to them. Clients construct the URI from the Manager's own @odata.id
// instead, with the manager id repeated as the final segment.
//
//	nv-redfish  src/oem/dell/attributes.rs
//	  // Dell doesn't provide navigation property to the Attributes from the
//	  // Manager. So we just craft @odata.id for it.
//	  format!("{}/Oem/Dell/DellAttributes/{}", manager.odata_id(), manager.base.id)
//
//	libredfish src/dell.rs
//	  format!("Managers/{manager_id}/Oem/Dell/DellAttributes/{manager_id}")
//
// So the path is fixed by the manager id and must not be hard-coded here.
const (
	dellAttributesPathTemplate = "/redfish/v1/Managers/%s/Oem/Dell/DellAttributes/%s"

	// libredfish probes the standard location first in several flows and falls
	// back to the OEM path on a 404. Serving both is what NVIDIA's own mock
	// does, and costs one extra route.
	dellStandardAttributesPathTemplate = "/redfish/v1/Managers/%s/Attributes"

	// Id is deliberately not the last segment of the URI. These are the values
	// the reference mock reports.
	dellAttributesID   = "iDRACAttributes"
	dellAttributesName = "OEMAttributeRegistry"
	dellAttributesType = "#DellAttributes.v1_0_0.DellAttributes"
)

// defaultDellAttributes is the set NVIDIA's mock serves, whose own comment
// notes it holds only the attributes libredfish requires.
//
// Every value is a string on purpose. The reader is str_value(), which yields
// None for any other JSON type, and the caller unwraps that with
// bmc_not_provided, so a number or a bool here fails exactly as if the
// attribute were missing.
//
// Of these, only Lockdown.1.SystemLockdown and Racadm.1.Enable are load-bearing
// for exploration: together they resolve the internal lockdown status to
// Disabled, the clean result rather than Partial. The rest are inert static
// data that keeps setup-status checks quiet.
func defaultDellAttributes() map[string]string {
	return map[string]string{
		"IPMILan.1.Enable":            "Enabled",
		"IPMISOL.1.BaudRate":          "115200",
		"IPMISOL.1.Enable":            "Enabled",
		"IPMISOL.1.MinPrivilege":      "Administrator",
		"Lockdown.1.SystemLockdown":   "Disabled",
		"OS-BMC.1.AdminState":         "Disabled",
		"Racadm.1.Enable":             "Enabled",
		"SSH.1.Enable":                "Enabled",
		"SerialRedirection.1.Enable":  "Enabled",
		"WebServer.1.HostHeaderCheck": "Disabled",
	}
}

// dellAttributesResource serves the iDRAC attribute registry for one manager.
//
// PATCH is served as well as GET because the reference mock serves both and
// because clients do write here; a PATCH merges the keys it carries rather
// than replacing the map, which is what a Redfish attribute PATCH means.
type dellAttributesResource struct {
	managerID string

	mu         sync.RWMutex
	attributes map[string]string
}

func newDellAttributesResource(managerID string) *dellAttributesResource {
	return &dellAttributesResource{
		managerID:  managerID,
		attributes: defaultDellAttributes(),
	}
}

func (d *dellAttributesResource) odataID() string {
	return fmt.Sprintf(dellAttributesPathTemplate, d.managerID, d.managerID)
}

// payload renders the resource. The shape is fixed by the reference mock: the
// canonical @odata.id is the OEM path even when the request arrived at the
// standard one, so a client that follows the id lands somewhere real.
func (d *dellAttributesResource) payload() map[string]any {
	d.mu.RLock()
	defer d.mu.RUnlock()

	attributes := make(map[string]any, len(d.attributes))
	for key, value := range d.attributes {
		attributes[key] = value
	}

	return map[string]any{
		"@odata.id":   d.odataID(),
		"@odata.type": dellAttributesType,
		"Id":          dellAttributesID,
		"Name":        dellAttributesName,
		"Attributes":  attributes,
	}
}

// merge applies a PATCH body. Only string values are accepted: storing anything
// else would serve an attribute that reads back as absent.
func (d *dellAttributesResource) merge(attributes map[string]json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for key, raw := range attributes {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			logrus.WithField("attribute", key).
				Warn("Ignoring non-string Dell attribute in PATCH")
			continue
		}
		d.attributes[key] = value
	}
}

func (d *dellAttributesResource) serveGet(w http.ResponseWriter, r *http.Request) {
	if id := mux.Vars(r)["managerId"]; id != d.managerID {
		http.NotFound(w, r)
		return
	}

	writeJSON(w, http.StatusOK, d.payload())
}

func (d *dellAttributesResource) servePatch(w http.ResponseWriter, r *http.Request) {
	if id := mux.Vars(r)["managerId"]; id != d.managerID {
		http.NotFound(w, r)
		return
	}

	var body struct {
		Attributes map[string]json.RawMessage `json:"Attributes"`
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

	d.merge(body.Attributes)

	writeJSON(w, http.StatusOK, d.payload())
}

// registerDellOemRoutes wires both attribute paths for the given manager.
//
// These are registered outside the generated route table because Redfish does
// not define them; they are a Dell OEM extension. They are registered behind
// the same auth middleware as every other Manager subresource.
func registerDellOemRoutes(router *mux.Router, authMiddleware mux.MiddlewareFunc, managerID string) {
	resource := newDellAttributesResource(managerID)

	oem := router.NewRoute().Subrouter()
	oem.Use(authMiddleware)

	for _, path := range []string{
		fmt.Sprintf(dellAttributesPathTemplate, "{managerId}", "{attributesId}"),
		fmt.Sprintf(dellStandardAttributesPathTemplate, "{managerId}"),
	} {
		oem.Methods(http.MethodGet).Path(path).HandlerFunc(resource.serveGet)
		oem.Methods(http.MethodPatch).Path(path).HandlerFunc(resource.servePatch)
	}

	logrus.WithField("odataId", resource.odataID()).Info("Serving Dell OEM attributes")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		logrus.WithError(err).Warn("Failed to write JSON response")
	}
}
