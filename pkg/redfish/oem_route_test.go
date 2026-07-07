package redfish

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

// TestOEMRouteReachableThroughRealRouter builds the production router exactly as
// the emulator does (generated routes on an auth subrouter + the custom OEM
// route) and confirms a request to /Systems/Bluefield/Oem/Nvidia actually
// reaches the OEM handler rather than being shadowed/404'd by the generated
// catch-all subrouter.
func TestOEMRouteReachableThroughRealRouter(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().IsDPU().Return(true).AnyTimes()
	mockRM.EXPECT().SystemOEM().Return("DPU", "02:00:00:00:00:02").AnyTimes()

	apiService := NewAPIService(testUsername, testPassword, mockRM)
	authMiddleware := session.AuthMiddleware(testUsername, testPassword)
	router := newRouter(apiService, authMiddleware)

	req := httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/Bluefield/Oem/Nvidia", nil)
	req.SetBasicAuth(testUsername, testPassword)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "OEM route must reach the handler, body=%q", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "BaseMAC")
	assert.Contains(t, rec.Body.String(), "Mode")
	assert.Contains(t, rec.Body.String(), "#NvidiaComputerSystem.v1_0_0.NvidiaComputerSystem")
}

// TestDPURedfishRoutesReachable builds the production router and confirms the DPU
// state-progression endpoints reach their handlers: SecureBoot and Bios reads go
// through the (now filled) generated stubs, and the BIOS/boot mutations go through
// the custom no-op routes. This guards against a generated catch-all shadowing a
// path or a missing custom-route registration.
func TestDPURedfishRoutesReachable(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	mockRM.EXPECT().IsDPU().Return(true).AnyTimes()

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

	sb := do(http.MethodGet, "/redfish/v1/Systems/Bluefield/SecureBoot")
	assert.Equal(t, http.StatusOK, sb.Code, "body=%q", sb.Body.String())
	assert.Contains(t, sb.Body.String(), `"SecureBootEnable":false`)
	assert.Contains(t, sb.Body.String(), `"SecureBootCurrentBoot":"Disabled"`)

	bios := do(http.MethodGet, "/redfish/v1/Systems/Bluefield/Bios")
	assert.Equal(t, http.StatusOK, bios.Code, "body=%q", bios.Body.String())
	assert.Contains(t, bios.Body.String(), "Restricted")
	assert.Contains(t, bios.Body.String(), "Embedded")

	assert.Equal(t, http.StatusNoContent, do(http.MethodPatch, "/redfish/v1/Systems/Bluefield/Settings").Code)
	assert.Equal(t, http.StatusNoContent, do(http.MethodPatch, "/redfish/v1/Systems/Bluefield/Bios/Settings").Code)
	assert.Equal(t, http.StatusNoContent, do(http.MethodPost, "/redfish/v1/Systems/Bluefield/Oem/Nvidia/Actions/HostRshim.Set").Code)
}
