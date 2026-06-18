package redfish

import (
	"encoding/json"
	"testing"
	"time"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
)

func TestSanitizeOmitsEmptyReferences(t *testing.T) {
	root := server.ServiceRootV1161ServiceRoot{
		OdataId:   "/redfish/v1",
		OdataType: "#ServiceRoot.v1_16_1.ServiceRoot",
		Name:      "ServiceRoot",
		Systems: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Systems",
		},
		Links: server.ServiceRootV1161Links{
			Sessions: server.OdataV4IdRef{
				OdataId: "/redfish/v1/SessionService/Sessions",
			},
		},
	}

	var out map[string]any
	if err := json.Unmarshal(sanitize(&root), &out); err != nil {
		t.Fatalf("unmarshal sanitized body: %v", err)
	}

	// Populated references survive and keep their @odata.id.
	systems, ok := out["Systems"].(map[string]any)
	if !ok || systems["@odata.id"] != "/redfish/v1/Systems" {
		t.Fatalf("expected populated Systems reference, got %#v", out["Systems"])
	}
	links, ok := out["Links"].(map[string]any)
	if !ok {
		t.Fatalf("expected Links object, got %#v", out["Links"])
	}
	if sessions, ok := links["Sessions"].(map[string]any); !ok || sessions["@odata.id"] != "/redfish/v1/SessionService/Sessions" {
		t.Fatalf("expected populated Sessions reference, got %#v", links["Sessions"])
	}

	// Unset references must be absent rather than serialized as `{}`.
	if _, present := out["AggregationService"]; present {
		t.Errorf("unset AggregationService reference must be omitted")
	}
	if _, present := out["Chassis"]; present {
		t.Errorf("unset Chassis reference must be omitted")
	}
	if _, present := links["ManagerProvidingService"]; present {
		t.Errorf("unset nested ManagerProvidingService reference must be omitted")
	}
}

func TestSanitizeManagerDateTimeAndEmptyConsoles(t *testing.T) {
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	mgr := server.ManagerV1190Manager{
		OdataId:     "/redfish/v1/Managers/BMC",
		OdataType:   "#Manager.v1_19_2.Manager",
		Id:          "BMC",
		Name:        "Manager",
		ManagerType: "BMC",
		DateTime:    &now,
		// CommandShell, GraphicalConsole, SerialConsole, Status, Links left zero.
	}

	var out map[string]any
	if err := json.Unmarshal(sanitize(&mgr), &out); err != nil {
		t.Fatalf("unmarshal sanitized body: %v", err)
	}

	// time.Time must serialize via its MarshalJSON as an RFC 3339 string,
	// not get reflected into `{}`.
	dt, ok := out["DateTime"].(string)
	if !ok {
		t.Fatalf("expected DateTime string, got %#v", out["DateTime"])
	}
	if _, err := time.Parse(time.RFC3339, dt); err != nil {
		t.Errorf("DateTime %q is not RFC 3339: %v", dt, err)
	}

	// Unset nested objects (omitempty structs) must be omitted, not `{}`.
	for _, field := range []string{"CommandShell", "GraphicalConsole", "SerialConsole", "Status"} {
		if _, present := out[field]; present {
			t.Errorf("unset %s must be omitted, got %#v", field, out[field])
		}
	}
}
