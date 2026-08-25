package redfish

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// asMap round-trips a resource through JSON so assertions run against the bytes
// a Redfish client actually receives, not against the Go struct. This matters
// for the empty-collection cases: `Members: []` and a `Members@odata.count` of
// 0 must both be *present* in the payload, and a Go nil slice or an omitempty
// tag would silently drop them.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// GET /redfish/v1/Chassis must be a 200 with an empty collection. ServiceRoot
// advertises the link, and libredfish treats the 501 it used to return as a
// hard error rather than as "unsupported".
func TestGetChassisCollection_ListsTheSystemChassis(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := asMap(t, h.GetChassisCollection())

	assert.Equal(t, "/redfish/v1/Chassis", body["@odata.id"])
	assert.Equal(t, "#ChassisCollection.ChassisCollection", body["@odata.type"])

	members, ok := body["Members"].([]any)
	require.True(t, ok, "Members must be present and be an array, got %T", body["Members"])

	// The collection was served empty for a long time, which was enough while
	// nothing walked it. The boot-option name now resolves through the chassis
	// -- Chassis/{system_id} to NetworkAdapters to NetworkDeviceFunctions to the
	// NIC's device description -- so the member has to exist, and its id has to
	// equal the ComputerSystem id, because that URL is fetched directly rather
	// than discovered from this listing.
	require.Len(t, members, 1)
	assert.Equal(t, "/redfish/v1/Chassis/1", members[0].(map[string]any)["@odata.id"])

	count, ok := body["Members@odata.count"].(float64)
	require.True(t, ok, "Members@odata.count must be present")
	assert.Equal(t, float64(1), count)
}

// UpdateService itself was a 501, which made FirmwareInventory unreachable no
// matter what FirmwareInventory returned: clients navigate to the inventory
// through this resource.
func TestGetUpdateService_ReachableAndDisabled(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	svc := h.GetUpdateService()
	body := asMap(t, svc)

	assert.Equal(t, "/redfish/v1/UpdateService", body["@odata.id"])
	require.NotNil(t, svc.ServiceEnabled)
	assert.False(t, *svc.ServiceEnabled, "virtbmc cannot flash firmware, so it must not claim it can")

	inv, ok := body["FirmwareInventory"].(map[string]any)
	require.True(t, ok, "UpdateService must link to FirmwareInventory")
	assert.Equal(t, "/redfish/v1/UpdateService/FirmwareInventory", inv["@odata.id"])
}

func TestGetFirmwareInventory_IsEmptyNotError(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := asMap(t, h.GetFirmwareInventory())

	assert.Equal(t, "/redfish/v1/UpdateService/FirmwareInventory", body["@odata.id"])

	members, ok := body["Members"].([]any)
	require.True(t, ok, "Members must be present and be an array, got %T", body["Members"])
	assert.Empty(t, members)

	count, ok := body["Members@odata.count"].(float64)
	require.True(t, ok, "Members@odata.count must be present")
	assert.Equal(t, float64(0), count)
}

func managerIfaces() []resourcemanager.EthernetInterfaceInterface {
	return []resourcemanager.EthernetInterfaceInterface{
		resourcemanager.NewEthernetInterface(
			"eth0", "eth0", "52:54:00:12:34:56", true,
			server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_UP,
		),
		resourcemanager.NewEthernetInterface(
			"eth1", "eth1", "52:54:00:12:34:57", true,
			server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_UP,
		),
	}
}

// The Manager-anchored collection is the path clients walk when they want the
// BMC's own NICs. It is a distinct resource from the System-anchored one, so
// its members must be Manager URLs, not System URLs.
func TestGetManagerEthernetInterfaceCollection_AnchorsUnderManager(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetEthernetInterfaces().Return(managerIfaces(), nil)

	h := NewHandler(testUsername, testPassword, mockRM)

	collection, err := h.GetManagerEthernetInterfaceCollection("BMC")
	require.NoError(t, err)

	assert.Equal(t, "/redfish/v1/Managers/BMC/EthernetInterfaces", collection.OdataId)
	assert.Equal(t, int64(2), collection.MembersodataCount)
	require.Len(t, collection.Members, 2)
	assert.Equal(t, "/redfish/v1/Managers/BMC/EthernetInterfaces/eth0", collection.Members[0].OdataId)
	assert.Equal(t, "/redfish/v1/Managers/BMC/EthernetInterfaces/eth1", collection.Members[1].OdataId)

	for _, m := range collection.Members {
		assert.NotContains(t, m.OdataId, "/Systems/",
			"Manager collection must not hand back System-anchored member URLs")
	}
}

// A member document must be self-consistent with the URL it was fetched from,
// and must carry the real MAC: NICo keys machines on the BMC MAC.
func TestGetManagerEthernetInterface_SelfConsistentAndCarriesMAC(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetEthernetInterfaces().Return(managerIfaces(), nil)

	h := NewHandler(testUsername, testPassword, mockRM)

	iface, err := h.GetManagerEthernetInterface("BMC", "eth1")
	require.NoError(t, err)

	assert.Equal(t, "/redfish/v1/Managers/BMC/EthernetInterfaces/eth1", iface.OdataId)
	assert.Equal(t, "eth1", iface.Id)
	assert.Equal(t, "52:54:00:12:34:57", iface.MACAddress)
	require.NotNil(t, iface.InterfaceEnabled)
	assert.True(t, *iface.InterfaceEnabled)
	assert.Equal(t, server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_UP, iface.LinkStatus)
}

func TestGetManagerEthernetInterface_UnknownIDErrors(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetEthernetInterfaces().Return(managerIfaces(), nil)

	h := NewHandler(testUsername, testPassword, mockRM)

	_, err := h.GetManagerEthernetInterface("BMC", "eth9")
	assert.Error(t, err)
}

// Members@odata.count must equal len(Members). It was left unset on both
// top-level collections, so they went out reporting one member and a count of
// 0; a client that trusts the count before iterating concludes there are no
// systems and no managers at all.
func TestGetComputerSystemCollection_CountMatchesMembers(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := asMap(t, h.GetComputerSystemCollection())

	assert.Equal(t, "/redfish/v1/Systems", body["@odata.id"])

	members, ok := body["Members"].([]any)
	require.True(t, ok, "Members must be present and be an array, got %T", body["Members"])
	require.Len(t, members, 1)

	count, ok := body["Members@odata.count"].(float64)
	require.True(t, ok, "Members@odata.count must be present")
	assert.Equal(t, float64(len(members)), count)
}

func TestGetManagerCollection_CountMatchesMembers(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := asMap(t, h.GetManagerCollection())

	assert.Equal(t, "/redfish/v1/Managers", body["@odata.id"])

	members, ok := body["Members"].([]any)
	require.True(t, ok, "Members must be present and be an array, got %T", body["Members"])
	require.Len(t, members, 1)

	count, ok := body["Members@odata.count"].(float64)
	require.True(t, ok, "Members@odata.count must be present")
	assert.Equal(t, float64(len(members)), count)
}
