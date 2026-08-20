package redfish

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// encodeAsServed runs a resource through the same EncodeJSONResponse the
// generated routes use, so these assertions see the bytes on the wire and not
// the output of a bare json.Marshal. The distinction is the whole point of the
// test: the empty-object pruning lives in the encoder.
func encodeAsServed(t *testing.T, v any) map[string]any {
	t.Helper()

	recorder := httptest.NewRecorder()
	require.NoError(t, server.EncodeJSONResponse(v, nil, recorder))

	decoder := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes()))
	decoder.UseNumber()

	var body map[string]any
	require.NoError(t, decoder.Decode(&body))

	return body
}

// emptyObjectPaths lists every property in the payload whose value is `{}`.
func emptyObjectPaths(root string, value any) []string {
	var found []string

	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			return []string{root}
		}
		names := make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			found = append(found, emptyObjectPaths(root+"."+name, typed[name])...)
		}
	case []any:
		for i, element := range typed {
			found = append(found, emptyObjectPaths(fmt.Sprintf("%s[%d]", root, i), element)...)
		}
	}

	return found
}

// assertNoEmptyReferences checks that no property in the payload is served as
// `{}` except the ones named in allowed.
//
// allowed holds only complex types -- ProtocolFeaturesSupported, Location,
// SerialConsole and friends -- which became empty because every property they
// carried was itself an empty reference that got dropped. Those are not
// references, so they have no mandatory `@odata.id` and `{}` deserializes. Any
// path that turns up here and is not on the list is a reference going out
// without its `@odata.id`, which is fatal to a strict client.
func assertNoEmptyReferences(t *testing.T, name string, body map[string]any, allowed ...string) {
	t.Helper()

	permitted := make(map[string]bool, len(allowed))
	for _, path := range allowed {
		permitted[name+"."+path] = true
	}

	for _, path := range emptyObjectPaths(name, body) {
		assert.True(t, permitted[path], "%s is served as {}; a Redfish reference must carry @odata.id", path)
	}
}

// The reported failure. NICo's site-explorer GETs /redfish/v1/ first and typed
// every navigation link as a reference, so `"AggregationService":{}` aborted
// the deserialization of the whole document:
//
//	NICO-SITEEXPLORER-130 missing field `@odata.id` at line 1 column 260
//
// Column 260 was AggregationService. virtbmc implements none of these services,
// and per Redfish the way to say so is to leave the property out.
func TestServiceRoot_OmitsUnimplementedServices(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := encodeAsServed(t, h.GetServiceRoot())

	unimplemented := []string{
		"AggregationService", "Cables", "CertificateService", "ComponentIntegrity",
		"Fabrics", "Facilities", "JobService", "JsonSchemas", "KeyService",
		"LicenseService", "NVMeDomains", "PowerEquipment", "RegisteredClients",
		"ResourceBlocks", "ServiceConditions", "Storage", "StorageServices",
		"StorageSystems", "ThermalEquipment",
	}
	for _, name := range unimplemented {
		assert.NotContains(t, body, name, "%s is not implemented, so it must be absent rather than {}", name)
	}

	// Advertised only where the endpoint is actually served. These five used
	// to be advertised and answered 501, which reads to a client walking the
	// service root as a broken BMC rather than as an unsupported feature.
	for _, name := range []string{
		"Registries", "AccountService", "EventService",
		"TelemetryService", "CompositionService",
	} {
		assert.NotContains(t, body, name, "%s answers 501, so it must not be advertised", name)
	}

	// The services that do exist keep their links; omission must not be
	// indiscriminate.
	implemented := map[string]string{
		"Chassis":        "/redfish/v1/Chassis",
		"Managers":       "/redfish/v1/Managers",
		"SessionService": "/redfish/v1/SessionService",
		"Systems":        "/redfish/v1/Systems",
		// The TaskService resource, not /redfish/v1/Tasks, which has no route.
		"Tasks":         "/redfish/v1/TaskService",
		"UpdateService": "/redfish/v1/UpdateService",
	}
	for name, odataID := range implemented {
		reference, ok := body[name].(map[string]any)
		require.True(t, ok, "%s must still be served as a reference, got %T", name, body[name])
		assert.Equal(t, odataID, reference["@odata.id"])
	}

	links, ok := body["Links"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "/redfish/v1/Managers/BMC", links["ManagerProvidingService"].(map[string]any)["@odata.id"])
	assert.Equal(t, "/redfish/v1/SessionService/Sessions", links["Sessions"].(map[string]any)["@odata.id"])

	// Id is required and carried no omitempty, so it used to go out as "".
	assert.Equal(t, "RootService", body["Id"])

	assertNoEmptyReferences(t, "ServiceRoot", body, "ProtocolFeaturesSupported")
}

// Fixing ServiceRoot alone would have moved the failure one hop: site-explorer
// walks on to the Systems and Managers it advertises, and those documents were
// serving the same empty references. Every resource on the walk has to hold.
func TestServedResources_CarryNoEmptyReferences(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	computerSystem := resourcemanager.NewComputerSystem(
		"1", "vm", "serial", "11111111-1111-1111-1111-111111111111",
		server.RESOURCEPOWERSTATE_ON, server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI,
	)
	manager := resourcemanager.NewManager("BMC", "Manager", "22222222-2222-2222-2222-222222222222")
	virtualMedia := resourcemanager.NewVirtualMedia("CD1", "CD1")

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetComputerSystem().Return(computerSystem, nil).AnyTimes()
	mockRM.EXPECT().GetManager().Return(manager, nil).AnyTimes()
	mockRM.EXPECT().GetVirtualMedia().Return(virtualMedia, nil).AnyTimes()
	mockRM.EXPECT().GetEthernetInterfaces().Return(managerIfaces(), nil).AnyTimes()

	h := NewHandler(testUsername, testPassword, mockRM)

	servedManager, err := h.GetManager()
	require.NoError(t, err)
	servedComputerSystem, err := h.GetComputerSystem()
	require.NoError(t, err)
	servedVirtualMedia, err := h.GetVirtualMedia()
	require.NoError(t, err)
	systemInterfaces, err := h.GetEthernetInterfaceCollection()
	require.NoError(t, err)
	managerInterfaces, err := h.GetManagerEthernetInterfaceCollection("BMC")
	require.NoError(t, err)
	managerInterface, err := h.GetManagerEthernetInterface("BMC", "eth0")
	require.NoError(t, err)

	// The allowed paths are complex types left empty because everything they
	// held was an unimplemented reference. None of them is a reference itself.
	cases := []struct {
		name     string
		resource any
		allowed  []string
	}{
		{name: "SessionService", resource: h.GetSessionService()},
		{name: "SessionCollection", resource: h.GetSessionCollection()},
		{name: "ManagerCollection", resource: h.GetManagerCollection()},
		{name: "ChassisCollection", resource: h.GetChassisCollection()},
		// UpdateService.Actions held four actions that were themselves empty --
		// SimpleUpdate and friends, with no target -- so the whole Actions
		// object emptied out. Actions is not a reference.
		{name: "UpdateService", resource: h.GetUpdateService(), allowed: []string{"Actions"}},
		{name: "FirmwareInventory", resource: h.GetFirmwareInventory()},
		{name: "VirtualMediaCollection", resource: h.GetVirtualMediaCollection()},
		{name: "ComputerSystemCollection", resource: h.GetComputerSystemCollection()},
		{name: "EthernetInterfaceCollection", resource: systemInterfaces},
		{name: "ManagerEthernetInterfaceCollection", resource: managerInterfaces},
		{name: "Manager", resource: servedManager, allowed: []string{"Links", "Location"}},
		{
			name:     "ComputerSystem",
			resource: servedComputerSystem,
			allowed:  []string{"HostWatchdogTimer", "HostedServices", "KeyManagement", "Links", "SerialConsole"},
		},
		{name: "VirtualMedia", resource: servedVirtualMedia},
		{name: "ManagerEthernetInterface", resource: managerInterface, allowed: []string{"Links"}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body := encodeAsServed(t, testCase.resource)
			assertNoEmptyReferences(t, testCase.name, body, testCase.allowed...)
		})
	}
}

// ComputerSystem serves the properties that were previously masked by the `{}`
// bug: pruning an unset field is right for a service that does not exist, but
// these do exist and had to be filled in rather than dropped.
func TestComputerSystem_LinksWhatItActuallyServes(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	computerSystem := resourcemanager.NewComputerSystem(
		"1", "vm", "serial", "11111111-1111-1111-1111-111111111111",
		server.RESOURCEPOWERSTATE_ON, server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI,
	)

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetComputerSystem().Return(computerSystem, nil)

	h := NewHandler(testUsername, testPassword, mockRM)

	served, err := h.GetComputerSystem()
	require.NoError(t, err)
	body := encodeAsServed(t, served)

	// The System-anchored NIC collection is implemented, so it must be
	// reachable rather than dropped as unsupported.
	ethernetInterfaces, ok := body["EthernetInterfaces"].(map[string]any)
	require.True(t, ok, "EthernetInterfaces must be a reference, got %T", body["EthernetInterfaces"])
	assert.Equal(t, "/redfish/v1/Systems/1/EthernetInterfaces", ethernetInterfaces["@odata.id"])

	// Redfish types OperatingSystem as a reference. It used to be serialized as
	// a bare URI string, which is a type error for a schema-driven client.
	operatingSystem, ok := body["OperatingSystem"].(map[string]any)
	require.True(t, ok, "OperatingSystem must be a reference, not a string, got %T", body["OperatingSystem"])
	assert.Equal(t, "/redfish/v1/Systems/1/OperatingSystem", operatingSystem["@odata.id"])

	status, ok := body["Status"].(map[string]any)
	require.True(t, ok, "Status must be populated, got %T", body["Status"])
	assert.Equal(t, "OK", status["Health"])
	assert.Equal(t, "Enabled", status["State"])
}

func TestManager_ReportsStatus(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	manager := resourcemanager.NewManager("BMC", "Manager", "22222222-2222-2222-2222-222222222222")

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetManager().Return(manager, nil)

	h := NewHandler(testUsername, testPassword, mockRM)

	served, err := h.GetManager()
	require.NoError(t, err)

	status, ok := encodeAsServed(t, served)["Status"].(map[string]any)
	require.True(t, ok, "Status must be populated")
	assert.Equal(t, "OK", status["Health"])
	assert.Equal(t, "Enabled", status["State"])
}

// Pruning must not touch empty collections. `Members: []` with a
// `Members@odata.count` of 0 is a meaningful answer -- "this collection exists
// and is empty" -- and dropping either half of it tells a client something
// false.
func TestEmptyCollection_KeepsMembersAndCount(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	// The task collection, not the chassis one: the chassis now carries a
	// member, because the boot-option name resolves through it.
	body := encodeAsServed(t, h.GetTaskCollection())

	members, ok := body["Members"].([]any)
	require.True(t, ok, "Members must survive as an array, got %T", body["Members"])
	assert.Empty(t, members)

	count, ok := body["Members@odata.count"].(json.Number)
	require.True(t, ok, "Members@odata.count must survive, got %T", body["Members@odata.count"])
	assert.Equal(t, "0", count.String())
}
