package redfish

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

const testBootMAC = "02:00:00:00:00:04"

func TestGetManagerNetworkProtocol(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// nico reads only IPMI.ProtocolEnabled; true makes EnableIpmiOverLan skip its
	// enable PATCH. The nested flag must survive serialization.
	np := h.GetManagerNetworkProtocol("BMC")
	assert.NotNil(t, np.IPMI.ProtocolEnabled)
	assert.True(t, *np.IPMI.ProtocolEnabled)
	assert.Equal(t, "/redfish/v1/Managers/BMC/NetworkProtocol", np.OdataId)

	body := string(sanitize(np))
	assert.Contains(t, body, `"IPMI":{"ProtocolEnabled":true}`)
}

func TestGetManagerHostOem(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// A host reports the iLO firmware version and Oem.Hpe.VirtualNICEnabled true,
	// which nico's lockdown_status needs (with Bios UsbBoot "Enabled") to read the
	// BMC as fully disabled.
	mockRM.EXPECT().GetManager().Return(resourcemanager.NewManager("BMC", "Manager"), nil)
	mockRM.EXPECT().IsDPU().Return(false)
	mgr, err := h.GetManager()
	assert.NoError(t, err)
	assert.NotNil(t, mgr.FirmwareVersion)
	assert.Equal(t, "iLO 6 v1.58", *mgr.FirmwareVersion)
	hpe, ok := mgr.Oem["Hpe"].(map[string]interface{})
	assert.True(t, ok)
	assert.Equal(t, true, hpe["VirtualNICEnabled"])

	// A DPU manager is left untouched (no HPE Oem injected).
	mockRM.EXPECT().GetManager().Return(resourcemanager.NewManager("BMC", "Manager"), nil)
	mockRM.EXPECT().IsDPU().Return(true)
	dpuMgr, err := h.GetManager()
	assert.NoError(t, err)
	assert.Nil(t, dpuMgr.Oem)
}

func TestGetBootOptionsHost(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// With a known boot MAC the collection lists the single boot option, and the
	// option's DisplayName carries HTTP + IPV4 + the uppercased colon MAC that
	// nico's SetBootOrder matches on.
	mockRM.EXPECT().IsDPU().Return(false)
	mockRM.EXPECT().HostBootMAC().Return(testBootMAC, true)
	col, err := h.GetBootOptionCollection()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), col.MembersodataCount)
	assert.Equal(t, "/redfish/v1/Systems/1/BootOptions/1", col.Members[0].OdataId)

	mockRM.EXPECT().IsDPU().Return(false)
	mockRM.EXPECT().HostBootMAC().Return(testBootMAC, true)
	opt, err := h.GetBootOption("1")
	assert.NoError(t, err)
	assert.NotNil(t, opt.DisplayName)
	upper := strings.ToUpper(*opt.DisplayName)
	assert.Contains(t, upper, "HTTP")
	assert.Contains(t, upper, "IPV4")
	assert.Contains(t, upper, strings.ToUpper(testBootMAC))
	assert.NotNil(t, opt.BootOptionReference)
	// The reference must equal Boot.BootOrder[0] (see GetComputerSystem) so nico's
	// CheckBootOrder gate passes.
	assert.Equal(t, hostBootOptionReference, *opt.BootOptionReference)

	// Without a boot MAC the collection is empty (serializes as [], not null) and
	// no option is served.
	mockRM.EXPECT().IsDPU().Return(false)
	mockRM.EXPECT().HostBootMAC().Return("", false)
	empty, err := h.GetBootOptionCollection()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), empty.MembersodataCount)
	assert.NotNil(t, empty.Members)
	assert.Contains(t, string(sanitize(empty)), `"Members":[]`)

	mockRM.EXPECT().IsDPU().Return(false)
	mockRM.EXPECT().HostBootMAC().Return("", false)
	_, err = h.GetBootOption("1")
	assert.Error(t, err)

	// A DPU never serves boot options.
	mockRM.EXPECT().IsDPU().Return(true)
	_, err = h.GetBootOptionCollection()
	assert.Error(t, err)
}

func TestGetComputerSystemHostBootOrder(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	cs := resourcemanager.NewComputerSystem("1", "ns/name", "HOST-BM2", "On", "UEFI")
	mockRM.EXPECT().GetComputerSystem().Return(cs, nil)
	mockRM.EXPECT().IsDPU().Return(false)
	mockRM.EXPECT().HostBootMAC().Return(testBootMAC, true)

	got, err := h.GetComputerSystem()
	assert.NoError(t, err)
	assert.Len(t, got.Boot.BootOrder, 1)
	assert.NotNil(t, got.Boot.BootOrder[0])
	assert.Equal(t, hostBootOptionReference, *got.Boot.BootOrder[0])
}

func TestHandleHostConfigNoop(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// A host accepts the BIOS/boot writes as no-ops; a DPU never receives them.
	mockRM.EXPECT().IsDPU().Return(false)
	rec := httptest.NewRecorder()
	h.HandleHostConfigNoop(rec, httptest.NewRequest(http.MethodPatch, "/redfish/v1/Systems/1/Bios/settings/", nil))
	assert.Equal(t, http.StatusNoContent, rec.Code)

	mockRM.EXPECT().IsDPU().Return(true)
	rec = httptest.NewRecorder()
	h.HandleHostConfigNoop(rec, httptest.NewRequest(http.MethodPatch, "/redfish/v1/Systems/Bluefield/Bios/settings/", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleHostBiosOemBoot(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// The three arrays must serialize as [] not null, or the client rejects the
	// whole body.
	mockRM.EXPECT().IsDPU().Return(false)
	rec := httptest.NewRecorder()
	h.HandleHostBiosOemBoot(rec, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/1/Bios/oem/hpe/boot/", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"PersistentBootConfigOrder":[]`)
	assert.Contains(t, body, `"BootSources":[]`)
	assert.Contains(t, body, `"DefaultBootOrder":[]`)
	assert.NotContains(t, body, "null")

	mockRM.EXPECT().IsDPU().Return(true)
	rec = httptest.NewRecorder()
	h.HandleHostBiosOemBoot(rec, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/Bluefield/Bios/oem/hpe/boot/", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHostRedfishRoutesReachable builds the production router and confirms the
// host HostInit endpoints reach their handlers: NetworkProtocol and BootOptions
// through the (now filled) generated stubs, and the lowercase/trailing-slash
// HPE BIOS/boot paths through the custom routes. Guards against a generated route
// shadowing a path or a missing custom-route registration.
func TestHostRedfishRoutesReachable(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().IsDPU().Return(false).AnyTimes()
	mockRM.EXPECT().HostBootMAC().Return(testBootMAC, true).AnyTimes()

	apiService := NewAPIService(testUsername, testPassword, mockRM)
	authMiddleware := session.AuthMiddleware(testUsername, testPassword)
	router := newRouter(apiService, authMiddleware)

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.SetBasicAuth(testUsername, testPassword)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	np := do(http.MethodGet, "/redfish/v1/Managers/BMC/NetworkProtocol")
	assert.Equal(t, http.StatusOK, np.Code, "body=%q", np.Body.String())
	assert.Contains(t, np.Body.String(), `"IPMI":{"ProtocolEnabled":true}`)

	col := do(http.MethodGet, "/redfish/v1/Systems/1/BootOptions")
	assert.Equal(t, http.StatusOK, col.Code, "body=%q", col.Body.String())

	opt := do(http.MethodGet, "/redfish/v1/Systems/1/BootOptions/1")
	assert.Equal(t, http.StatusOK, opt.Code, "body=%q", opt.Body.String())
	assert.Contains(t, strings.ToUpper(opt.Body.String()), strings.ToUpper(testBootMAC))

	boot := do(http.MethodGet, "/redfish/v1/Systems/1/Bios/oem/hpe/boot/")
	assert.Equal(t, http.StatusOK, boot.Code, "body=%q", boot.Body.String())

	// libredfish uses both the trailing- and non-trailing-slash forms of
	// Bios/settings; both must reach the no-op handler directly (no StrictSlash
	// 301, which would drop the PATCH to a GET).
	assert.Equal(t, http.StatusNoContent, do(http.MethodPatch, "/redfish/v1/Systems/1/Bios/settings").Code)
	assert.Equal(t, http.StatusNoContent, do(http.MethodPatch, "/redfish/v1/Systems/1/Bios/settings/").Code)
	assert.Equal(t, http.StatusNoContent, do(http.MethodPatch, "/redfish/v1/Systems/1/Bios/oem/hpe/boot/settings/").Code)
}
