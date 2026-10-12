package redfish

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

const (
	testBootMAC       = "02:00:00:00:00:04"
	testHostManagerID = "BMC"
)

func TestGetManagerNetworkProtocol(t *testing.T) {
	h, _ := newTestHandler(t, testHostIdentity)

	// nico reads only IPMI.ProtocolEnabled; true makes EnableIpmiOverLan skip its
	// enable PATCH. The nested flag must survive serialization.
	np := h.GetManagerNetworkProtocol(testHostManagerID)
	assert.NotNil(t, np.IPMI.ProtocolEnabled)
	assert.True(t, *np.IPMI.ProtocolEnabled)
	assert.Equal(t, "/redfish/v1/Managers/BMC/NetworkProtocol", np.OdataId)

	body := string(sanitize(np))
	assert.Contains(t, body, `"IPMI":{"ProtocolEnabled":true}`)
}

func TestGetManager(t *testing.T) {
	// A host reports the iLO firmware version and Oem.Hpe.VirtualNICEnabled true,
	// which nico's lockdown_status needs (with Bios UsbBoot "Enabled") to read the
	// BMC as fully disabled.
	h, mockRM := newTestHandler(t, testHostIdentity)
	mockRM.EXPECT().GetManager().Return(resourcemanager.NewManager(testHostManagerID, "Manager"), nil)
	mgr, err := h.GetManager()
	require.NoError(t, err)
	assert.Equal(t, "iLO 6 v1.58", *mgr.FirmwareVersion)
	hpe, ok := mgr.Oem["Hpe"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, hpe["VirtualNICEnabled"])
	assert.NotNil(t, mgr.DateTime)

	// A DPU reports its annotated BMC firmware and no HPE Oem.
	h, mockRM = newTestHandler(t, testDPUIdentity)
	mockRM.EXPECT().GetManager().Return(resourcemanager.NewManager("Bluefield_BMC", "Manager"), nil)
	dpuMgr, err := h.GetManager()
	require.NoError(t, err)
	assert.Equal(t, "BF-25.10-21", *dpuMgr.FirmwareVersion)
	assert.Nil(t, dpuMgr.Oem)
}

func TestGetBootOptionsHost(t *testing.T) {
	h, _ := newTestHandler(t, testHostIdentity)

	// With a known boot MAC the collection lists the single boot option, and the
	// option's DisplayName carries HTTP + IPV4 + the uppercased colon MAC that
	// nico's SetBootOrder matches on.
	col, err := h.GetBootOptionCollection()
	require.NoError(t, err)
	assert.Equal(t, int64(1), col.MembersodataCount)
	assert.Equal(t, "/redfish/v1/Systems/1/BootOptions/1", col.Members[0].OdataId)

	opt, err := h.GetBootOption("1")
	require.NoError(t, err)
	upper := strings.ToUpper(*opt.DisplayName)
	assert.Contains(t, upper, "HTTP")
	assert.Contains(t, upper, "IPV4")
	assert.Contains(t, upper, strings.ToUpper(testBootMAC))
	// The reference must equal Boot.BootOrder[0] (see GetComputerSystem) so nico's
	// CheckBootOrder gate passes.
	assert.Equal(t, hostBootOptionReference, *opt.BootOptionReference)

	// Without a boot MAC the collection is empty (serializes as [], not null) and
	// no option is served.
	noMAC := testHostIdentity
	noMAC.HostBootMAC = ""
	h, _ = newTestHandler(t, noMAC)
	empty, err := h.GetBootOptionCollection()
	require.NoError(t, err)
	assert.Equal(t, int64(0), empty.MembersodataCount)
	assert.Contains(t, string(sanitize(empty)), `"Members":[]`)
	_, err = h.GetBootOption("1")
	assert.Error(t, err)

	// A DPU never serves boot options.
	h, _ = newTestHandler(t, testDPUIdentity)
	_, err = h.GetBootOptionCollection()
	assert.Error(t, err)
}

func TestGetComputerSystemHostBootOrder(t *testing.T) {
	h, mockRM := newTestHandler(t, testHostIdentity)

	cs := resourcemanager.NewComputerSystem("1", "ns/name", "HOST-BM2", "On", "UEFI")
	mockRM.EXPECT().GetComputerSystem().Return(cs, nil)

	got, err := h.GetComputerSystem()
	require.NoError(t, err)
	require.Len(t, got.Boot.BootOrder, 1)
	assert.Equal(t, hostBootOptionReference, *got.Boot.BootOrder[0])
}

func TestHandleHostConfigNoop(t *testing.T) {
	// A host accepts the BIOS/boot writes as no-ops; a DPU never receives them.
	h, _ := newTestHandler(t, testHostIdentity)
	rec := httptest.NewRecorder()
	h.HandleHostConfigNoop(rec, httptest.NewRequest(http.MethodPatch, "/redfish/v1/Systems/1/Bios/settings/", nil))
	assert.Equal(t, http.StatusNoContent, rec.Code)

	h, _ = newTestHandler(t, testDPUIdentity)
	rec = httptest.NewRecorder()
	h.HandleHostConfigNoop(rec, httptest.NewRequest(http.MethodPatch, "/redfish/v1/Systems/Bluefield/Bios/settings/", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleHostBiosOemBoot(t *testing.T) {
	// The three arrays must serialize as [] not null, or the client rejects the
	// whole body.
	h, _ := newTestHandler(t, testHostIdentity)
	rec := httptest.NewRecorder()
	h.HandleHostBiosOemBoot(rec, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/1/Bios/oem/hpe/boot/", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"PersistentBootConfigOrder":[]`)
	assert.Contains(t, body, `"BootSources":[]`)
	assert.Contains(t, body, `"DefaultBootOrder":[]`)
	assert.NotContains(t, body, "null")

	h, _ = newTestHandler(t, testDPUIdentity)
	rec = httptest.NewRecorder()
	h.HandleHostBiosOemBoot(rec, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/Bluefield/Bios/oem/hpe/boot/", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHostRedfishRoutesReachable builds the production router and confirms the
// host HostInit endpoints reach their handlers: NetworkProtocol and BootOptions
// through the generated stubs, and the lowercase/trailing-slash HPE BIOS/boot
// paths through the custom routes. Guards against a generated route shadowing a
// path or a missing custom-route registration.
func TestHostRedfishRoutesReachable(t *testing.T) {
	_, mockRM := newTestHandler(t, testHostIdentity)
	router := newTestRouter(mockRM)

	np := doRequest(router, http.MethodGet, "/redfish/v1/Managers/BMC/NetworkProtocol", "")
	assert.Equal(t, http.StatusOK, np.Code, "body=%q", np.Body.String())
	assert.Contains(t, np.Body.String(), `"IPMI":{"ProtocolEnabled":true}`)

	col := doRequest(router, http.MethodGet, "/redfish/v1/Systems/1/BootOptions", "")
	assert.Equal(t, http.StatusOK, col.Code, "body=%q", col.Body.String())

	opt := doRequest(router, http.MethodGet, "/redfish/v1/Systems/1/BootOptions/1", "")
	assert.Equal(t, http.StatusOK, opt.Code, "body=%q", opt.Body.String())
	assert.Contains(t, strings.ToUpper(opt.Body.String()), strings.ToUpper(testBootMAC))

	boot := doRequest(router, http.MethodGet, "/redfish/v1/Systems/1/Bios/oem/hpe/boot/", "")
	assert.Equal(t, http.StatusOK, boot.Code, "body=%q", boot.Body.String())

	// libredfish uses both the trailing- and non-trailing-slash forms of
	// Bios/settings; both must reach the no-op handler directly (no StrictSlash
	// 301, which would drop the PATCH to a GET).
	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPatch, "/redfish/v1/Systems/1/Bios/settings", "{}").Code)
	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPatch, "/redfish/v1/Systems/1/Bios/settings/", "{}").Code)
	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPatch, "/redfish/v1/Systems/1/Bios/oem/hpe/boot/settings/", "{}").Code)
}
