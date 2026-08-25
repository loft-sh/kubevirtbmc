package redfish

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

func bootHandler(t *testing.T) *handler {
	t.Helper()

	ctl := gomock.NewController(t)
	t.Cleanup(ctl.Finish)

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetEthernetInterfaces().Return([]resourcemanager.EthernetInterfaceInterface{
		resourcemanager.NewEthernetInterface("default", "default", "02:00:00:00:04:01", true,
			server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_UP),
	}, nil).AnyTimes()

	return NewHandler(testUsername, testPassword, mockRM)
}

// THE load-bearing invariant of this whole surface.
//
// A client derives the name it expects as "HTTP Device 1: " + the NIC's
// DeviceDescription, then compares it to the first boot order entry's
// DisplayName for EXACT equality, tolerating only a trailing " - <suffix>".
// Both sides are served by us, so they must be built from one value. If they
// ever disagree the match can never succeed, and the failure is not idle: the
// client tries to FIX the boot order by writing BIOS and driving a job cycle,
// and the stuck-phase watchdog power-cycles the host on a timer.
func TestBootOption_DisplayNameMatchesTheServedDeviceDescription(t *testing.T) {
	h := bootHandler(t)

	function, err := h.GetNetworkDeviceFunction("1", "NIC.Slot.1", "default")
	require.NoError(t, err)

	description := function["Oem"].(map[string]any)["Dell"].(map[string]any)["DellNIC"].(map[string]any)["DeviceDescription"]
	require.NotEmpty(t, description)

	expected := "HTTP Device 1: " + description.(string)

	option, err := h.GetBootOption("1", "Boot0000")
	require.NoError(t, err)
	require.NotNil(t, option.DisplayName)

	assert.Equal(t, expected, *option.DisplayName,
		"the served DisplayName and the name derived from DeviceDescription must be identical")

	// Guard the exact-match rule itself: no leading or trailing space, and the
	// only tolerated deviation is a " - " suffix, which we do not use.
	assert.Equal(t, strings.TrimSpace(*option.DisplayName), *option.DisplayName)
	assert.NotContains(t, *option.DisplayName, " - ")
}

// The NDF Id must equal the host interface id: that equality is how a client
// matches an interface to the function carrying its description.
func TestNetworkDeviceFunction_IdEqualsInterfaceId(t *testing.T) {
	h := bootHandler(t)

	function, err := h.GetNetworkDeviceFunction("1", "NIC.Slot.1", "default")
	require.NoError(t, err)

	assert.Equal(t, "default", function["Id"])
	assert.Equal(t, "02:00:00:00:04:01", function["Ethernet"].(map[string]any)["MACAddress"])

	// Each missing layer of this nesting has its own distinct client-side
	// error, so all three levels have to be present.
	oem, ok := function["Oem"].(map[string]any)
	require.True(t, ok, "Oem is required")
	dell, ok := oem["Dell"].(map[string]any)
	require.True(t, ok, "Oem.Dell is required")
	nic, ok := dell["DellNIC"].(map[string]any)
	require.True(t, ok, "Oem.Dell.DellNIC is required")
	assert.NotEmpty(t, nic["DeviceDescription"])

	_, err = h.GetNetworkDeviceFunction("1", "NIC.Slot.1", "eth9")
	assert.Error(t, err, "an unserved function must not be fabricated")
}

// The chassis id must equal the ComputerSystem id, because that URL is fetched
// directly rather than discovered.
func TestChassis_IdEqualsComputerSystemId(t *testing.T) {
	h := bootHandler(t)

	chassis, err := h.GetChassis(resourcemanager.DefaultComputerSystemId)
	require.NoError(t, err)

	assert.Equal(t, resourcemanager.DefaultComputerSystemId, chassis["Id"])
	assert.Equal(t, "RackMount", chassis["ChassisType"])
	assert.Equal(t, "/redfish/v1/Chassis/1/NetworkAdapters",
		chassis["NetworkAdapters"].(map[string]any)["@odata.id"])

	_, err = h.GetChassis("System.Embedded.1")
	assert.Error(t, err, "only the system's own chassis id is served")
}

// Adding a chassis member creates surface a client can walk. Anything not
// served must be absent rather than advertised: absent means unsupported and is
// handled, while an advertised link that answers 501 is fatal.
func TestChassis_OmitsWhatItDoesNotServe(t *testing.T) {
	h := bootHandler(t)

	chassis, err := h.GetChassis("1")
	require.NoError(t, err)

	for _, property := range []string{
		"Power", "Thermal", "Sensors", "PCIeDevices", "Assembly",
		"PowerSubsystem", "ThermalSubsystem", "Drives", "MediaControllers",
	} {
		assert.NotContains(t, chassis, property,
			"%s is not served, so advertising it would be fatal to a client that walks it", property)
	}
}

// Boot.BootOrder holds reference STRINGS matched by equality against each
// member's BootOptionReference, and only index 0 is inspected. The nav property
// lives inside Boot, not at the top level of the system.
func TestComputerSystem_BootOrderNamesTheHttpOptionFirst(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	computerSystem := resourcemanager.NewComputerSystem(
		resourcemanager.DefaultComputerSystemId, "vm", "NICODEMO-0001",
		"11111111-1111-1111-1111-111111111111",
		server.RESOURCEPOWERSTATE_ON, server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI,
	)

	mockRM := resourcemanager.NewMockResourceManager(ctl)
	mockRM.EXPECT().GetComputerSystem().Return(computerSystem, nil).AnyTimes()
	mockRM.EXPECT().GetEthernetInterfaces().Return([]resourcemanager.EthernetInterfaceInterface{
		resourcemanager.NewEthernetInterface("default", "default", "02:00:00:00:04:01", true,
			server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_UP),
	}, nil).AnyTimes()

	h := NewHandler(testUsername, testPassword, mockRM)

	served, err := h.GetComputerSystem()
	require.NoError(t, err)
	body := encodeAsServed(t, served)

	boot, ok := body["Boot"].(map[string]any)
	require.True(t, ok)

	// Inside Boot, not at the top level.
	assert.NotContains(t, body, "BootOptions")
	assert.Equal(t, "/redfish/v1/Systems/1/BootOptions",
		boot["BootOptions"].(map[string]any)["@odata.id"])

	// Reference strings, not @odata.id objects. Omitting this parses as an
	// empty list on the client, which never matches.
	order, ok := boot["BootOrder"].([]any)
	require.True(t, ok, "BootOrder must be an array, got %T", boot["BootOrder"])
	require.NotEmpty(t, order)
	assert.Equal(t, "Boot0000", order[0], "the HTTP boot option must be first")

	// The first reference must resolve against the collection, since unmatched
	// references are silently dropped and would shift what lands first.
	collection := encodeAsServed(t, h.GetBootOptionCollection("1"))
	members, ok := collection["Members"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, members)
	assert.Equal(t, fmt.Sprintf("/redfish/v1/Systems/1/BootOptions/%s", order[0]),
		members[0].(map[string]any)["@odata.id"])

	// SecureBoot is advertised now that it is served: the boot-order step reads
	// it and will not proceed without it.
	assert.Equal(t, "/redfish/v1/Systems/1/SecureBoot",
		body["SecureBoot"].(map[string]any)["@odata.id"])

	// These three answer 501, so they must not be advertised.
	for _, property := range []string{"NetworkInterfaces", "SimpleStorage", "VirtualMedia"} {
		assert.NotContains(t, body, property, "%s answers 501, so advertising it is fatal", property)
	}
}

// Every mandatory member field must be present: they are non-optional strings
// in the client's model, so an incomplete member fails deserialization and
// takes the whole collection with it.
func TestBootOption_CarriesEveryMandatoryField(t *testing.T) {
	h := bootHandler(t)

	option, err := h.GetBootOption("1", "Boot0000")
	require.NoError(t, err)

	body := encodeAsServed(t, option)
	for _, field := range []string{"Id", "Name", "BootOptionReference", "DisplayName"} {
		value, present := body[field]
		require.True(t, present, "%s is mandatory", field)
		assert.NotEmpty(t, value, "%s must not be empty", field)
	}

	assert.Equal(t, "Boot0000", body["BootOptionReference"])
	assert.Equal(t, true, body["BootOptionEnabled"])

	_, err = h.GetBootOption("1", "Boot9999")
	assert.Error(t, err)
}

// SecureBoot is not tolerated as a failure on the boot-order path, unlike
// during exploration, so it has to answer with a complete resource.
func TestSecureBoot_ServedWithCompleteShape(t *testing.T) {
	h := bootHandler(t)

	body := encodeAsServed(t, h.GetSecureBoot("1"))

	assert.Equal(t, "/redfish/v1/Systems/1/SecureBoot", body["@odata.id"])
	assert.Equal(t, "SecureBoot", body["Id"])
	assert.NotEmpty(t, body["Name"])
	// Mandatory in every client model checked so far, even where the schema
	// calls it optional.
	assert.Equal(t, "/redfish/v1/$metadata#SecureBoot.SecureBoot", body["@odata.context"])
	// Reported disabled: nothing here verifies signatures, so claiming secure
	// boot is active would be a lie a caller might act on.
	assert.Equal(t, false, body["SecureBootEnable"])
	assert.Equal(t, "Disabled", body["SecureBootCurrentBoot"])
	assert.Equal(t, "UserMode", body["SecureBootMode"])
}

// The device description is the single knob, so that both ends move together.
func TestNICDeviceDescription_IsConfigurable(t *testing.T) {
	t.Setenv(NICDeviceDescriptionEnvVar, "NIC in Slot 5 Port 1")

	h := bootHandler(t)

	option, err := h.GetBootOption("1", "Boot0000")
	require.NoError(t, err)
	assert.Equal(t, "HTTP Device 1: NIC in Slot 5 Port 1", *option.DisplayName)

	function, err := h.GetNetworkDeviceFunction("1", "NIC.Slot.1", "default")
	require.NoError(t, err)
	assert.Equal(t, "NIC in Slot 5 Port 1",
		function["Oem"].(map[string]any)["Dell"].(map[string]any)["DellNIC"].(map[string]any)["DeviceDescription"])
}
