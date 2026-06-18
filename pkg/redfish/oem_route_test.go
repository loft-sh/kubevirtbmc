package redfish

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
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
	apiController := server.NewDefaultAPIController(apiService)
	authMiddleware := session.AuthMiddleware(testUsername, testPassword)
	router := server.NewRouter(authMiddleware, apiController)
	router.
		Methods(http.MethodGet).
		Path("/redfish/v1/Systems/{ComputerSystemId}/Oem/Nvidia").
		Handler(authMiddleware(http.HandlerFunc(apiService.handler.HandleSystemOEM)))

	req := httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/Bluefield/Oem/Nvidia", nil)
	req.SetBasicAuth(testUsername, testPassword)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "OEM route must reach the handler, body=%q", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "BaseMAC")
	assert.Contains(t, rec.Body.String(), "Mode")
	assert.Contains(t, rec.Body.String(), "#NvidiaComputerSystem.v1_0_0.NvidiaComputerSystem")
}
