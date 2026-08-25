package redfish

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

// The first thing a machine controller does with a new host is read
// IPMI.ProtocolEnabled from here. A missing IPMI object and a missing
// ProtocolEnabled field are both read as a failed request rather than as
// "disabled", so either omission strands the host on step one instead of
// degrading gracefully.
func TestNetworkProtocol_ReportsIPMIEnabled(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := encodeAsServed(t, h.GetManagerNetworkProtocol(resourcemanager.DefaultManagerId))

	assert.Equal(t, "/redfish/v1/Managers/BMC/NetworkProtocol", body["@odata.id"])
	assert.Equal(t, "NetworkProtocol", body["Id"])
	assert.Equal(t, "Manager Network Protocol", body["Name"])

	ipmi, ok := body["IPMI"].(map[string]any)
	require.True(t, ok, "IPMI must be an object, got %T", body["IPMI"])

	enabled, present := ipmi["ProtocolEnabled"]
	require.True(t, present, "IPMI.ProtocolEnabled must be present, not merely truthy-by-absence")
	assert.Equal(t, true, enabled, "reported disabled would cost an extra PATCH round trip")
}

// NTPServers must serialize even when empty. With omitempty, "subscribed to no
// servers" and "does not report NTP servers" collapse into the same payload.
func TestNetworkProtocol_NTPServersPresentWhenEmpty(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	ntp, ok := encodeAsServed(t, h.GetManagerNetworkProtocol("BMC"))["NTP"].(map[string]any)
	require.True(t, ok)

	servers, present := ntp["NTPServers"]
	require.True(t, present, "NTPServers must be present even when empty")
	assert.Empty(t, servers)
	assert.Equal(t, true, ntp["ProtocolEnabled"])
}

// The resource is writable and a PATCH is partial: a property the body does
// not mention must be left alone rather than zeroed.
func TestNetworkProtocol_PatchMergesNTPServers(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	patched := h.PatchManagerNetworkProtocol("BMC", server.ManagerNetworkProtocolV1100ManagerNetworkProtocol{
		NTP: server.ManagerNetworkProtocolV1100NtpProtocol{
			ProtocolEnabled: util.Ptr(true),
			NTPServers:      []*string{util.Ptr("192.168.100.1"), util.Ptr("time.example.com")},
		},
	})

	ntp, ok := encodeAsServed(t, patched)["NTP"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"192.168.100.1", "time.example.com"}, ntp["NTPServers"])

	// Untouched by this PATCH, and must not have been reset.
	ipmi, ok := encodeAsServed(t, patched)["IPMI"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, ipmi["ProtocolEnabled"], "a PATCH of NTP must not disturb IPMI")

	// Readable back on a subsequent GET, not just in the PATCH response.
	reread, ok := encodeAsServed(t, h.GetManagerNetworkProtocol("BMC"))["NTP"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{"192.168.100.1", "time.example.com"}, reread["NTPServers"])
}

// A PATCH that disables IPMI must take effect: the field is writable, and a
// client that turns it off and reads back a stubborn `true` is being lied to.
func TestNetworkProtocol_PatchTogglesIPMI(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	h.PatchManagerNetworkProtocol("BMC", server.ManagerNetworkProtocolV1100ManagerNetworkProtocol{
		IPMI: server.ManagerNetworkProtocolV1100Protocol{ProtocolEnabled: util.Ptr(false)},
	})

	ipmi := encodeAsServed(t, h.GetManagerNetworkProtocol("BMC"))["IPMI"].(map[string]any)
	assert.Equal(t, false, ipmi["ProtocolEnabled"])

	h.PatchManagerNetworkProtocol("BMC", server.ManagerNetworkProtocolV1100ManagerNetworkProtocol{
		IPMI: server.ManagerNetworkProtocolV1100Protocol{ProtocolEnabled: util.Ptr(true)},
	})

	ipmi = encodeAsServed(t, h.GetManagerNetworkProtocol("BMC"))["IPMI"].(map[string]any)
	assert.Equal(t, true, ipmi["ProtocolEnabled"])
}

// An empty PATCH body is the shape the generated router used to reject
// outright, by asserting the resource's required properties against a partial
// body. It must now be a no-op rather than an error.
func TestNetworkProtocol_EmptyPatchChangesNothing(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	before := encodeAsServed(t, h.GetManagerNetworkProtocol("BMC"))
	after := encodeAsServed(t, h.PatchManagerNetworkProtocol("BMC",
		server.ManagerNetworkProtocolV1100ManagerNetworkProtocol{}))

	assert.Equal(t, before, after)
}

// Every protocol object must survive the empty-object pruning, which means
// each has to carry at least ProtocolEnabled. An object pruned to nothing
// would read as an unsupported protocol.
func TestNetworkProtocol_ProtocolObjectsSurvivePruning(t *testing.T) {
	h := NewHandler(testUsername, testPassword, nil)

	body := encodeAsServed(t, h.GetManagerNetworkProtocol("BMC"))

	for _, name := range []string{"IPMI", "NTP", "HTTP", "HTTPS", "SSH", "Telnet", "KVMIP", "VirtualMedia"} {
		protocol, ok := body[name].(map[string]any)
		require.True(t, ok, "%s must be an object, got %T", name, body[name])
		_, present := protocol["ProtocolEnabled"]
		assert.True(t, present, "%s.ProtocolEnabled must be present", name)
	}
}

// The Manager has to advertise the link, or nothing reaches the resource. It
// was unset for a long time, which meant `{}` and then, once empty objects
// were dropped, nothing at all.
func TestManager_AdvertisesNetworkProtocol(t *testing.T) {
	manager := resourcemanager.NewManager(resourcemanager.DefaultManagerId, "Manager",
		"22222222-2222-2222-2222-222222222222")

	body := encodeAsServed(t, manager.Manager())

	link, ok := body["NetworkProtocol"].(map[string]any)
	require.True(t, ok, "NetworkProtocol must be advertised, got %T", body["NetworkProtocol"])
	assert.Equal(t, "/redfish/v1/Managers/BMC/NetworkProtocol", link["@odata.id"])
}
