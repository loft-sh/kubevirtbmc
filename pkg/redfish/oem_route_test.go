package redfish

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

// newTestRouter builds the production router exactly as the emulator does.
func newTestRouter(rm resourcemanager.ResourceManager) *mux.Router {
	apiService := NewAPIService(testUsername, testPassword, rm)

	return newRouter(apiService, session.AuthMiddleware(apiService.handler.VerifyCredentials))
}

func doRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.SetBasicAuth(testUsername, testPassword)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestDPURedfishRoutesReachable confirms the DPU endpoints nico drives reach
// their handlers through the real router: the Oem resource, SecureBoot and Bios
// reads through the generated stubs, and the mutations through the custom routes.
// It guards against a generated catch-all shadowing a path or a missing route.
func TestDPURedfishRoutesReachable(t *testing.T) {
	_, mockRM := newTestHandler(t, testDPUIdentity)
	router := newTestRouter(mockRM)

	oem := doRequest(router, http.MethodGet, "/redfish/v1/Systems/Bluefield/Oem/Nvidia", "")
	assert.Equal(t, http.StatusOK, oem.Code, "body=%q", oem.Body.String())
	assert.Contains(t, oem.Body.String(), `"BaseMAC":"020000000001"`)
	assert.Contains(t, oem.Body.String(), "#NvidiaComputerSystem.v1_0_0.NvidiaComputerSystem")

	sb := doRequest(router, http.MethodGet, "/redfish/v1/Systems/Bluefield/SecureBoot", "")
	assert.Equal(t, http.StatusOK, sb.Code, "body=%q", sb.Body.String())
	assert.Contains(t, sb.Body.String(), `"SecureBootEnable":false`)
	assert.Contains(t, sb.Body.String(), `"SecureBootCurrentBoot":"Disabled"`)

	bios := doRequest(router, http.MethodGet, "/redfish/v1/Systems/Bluefield/Bios", "")
	assert.Equal(t, http.StatusOK, bios.Code, "body=%q", bios.Body.String())
	assert.Contains(t, bios.Body.String(), "Restricted")

	fw := doRequest(router, http.MethodGet, "/redfish/v1/UpdateService/FirmwareInventory/DPU_SYS_IMAGE", "")
	assert.Equal(t, http.StatusOK, fw.Code, "body=%q", fw.Body.String())
	assert.Contains(t, fw.Body.String(), "0200:0000:0000:0001")

	mockRM.EXPECT().SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP).Return(nil)
	settings := doRequest(router, http.MethodPatch, "/redfish/v1/Systems/Bluefield/Settings",
		`{"Boot":{"BootSourceOverrideTarget":"UefiHttp","BootSourceOverrideEnabled":"Once"}}`)
	assert.Equal(t, http.StatusNoContent, settings.Code, "body=%q", settings.Body.String())

	mockRM.EXPECT().SetHostRshim(true)
	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPost, "/redfish/v1/Systems/Bluefield/Oem/Nvidia/Actions/HostRshim.Set", `{"HostRshim":"Enabled"}`).Code)

	mockRM.EXPECT().SetMode("NicMode")
	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPost, "/redfish/v1/Systems/Bluefield/Oem/Nvidia/Actions/Mode.Set", `{"Mode":"NicMode"}`).Code)

	mockRM.EXPECT().SetSecureBoot(true)
	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPatch, "/redfish/v1/Systems/Bluefield/SecureBoot", `{"SecureBootEnable":true}`).Code)

	cert := doRequest(router, http.MethodPost, "/redfish/v1/Systems/Bluefield/SecureBoot/SecureBootDatabases/PK/Certificates", `{"CertificateString":"x","CertificateType":"PEM"}`)
	assert.Equal(t, http.StatusAccepted, cert.Code, "body=%q", cert.Body.String())

	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPatch, "/redfish/v1/Systems/Bluefield/Bios/Settings", `{"Attributes":{}}`).Code)
	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPatch, "/redfish/v1/Managers/Bluefield_BMC/Oem/Nvidia", `{"BmcRShim":{"BmcRShimEnabled":true}}`).Code)
	assert.Equal(t, http.StatusNoContent, doRequest(router, http.MethodPatch, "/redfish/v1/AccountService", `{"AccountLockoutThreshold":0}`).Code)
}

// TestPasswordRotationThroughRouter walks nico's credential rotation: the factory
// password works, a PATCH of the account replaces it, and from then on only the
// new password authenticates.
func TestPasswordRotationThroughRouter(t *testing.T) {
	var hash string
	mockRM := resourcemanager.NewMockResourceManager(gomock.NewController(t))
	mockRM.EXPECT().Identity().Return(testDPUIdentity).AnyTimes()
	mockRM.EXPECT().PasswordHash().DoAndReturn(func() string { return hash }).AnyTimes()
	mockRM.EXPECT().SetPasswordHash(gomock.Any()).DoAndReturn(func(h string) error {
		hash = h
		return nil
	})
	router := newTestRouter(mockRM)

	accounts := doRequest(router, http.MethodGet, "/redfish/v1/AccountService/Accounts", "")
	assert.Equal(t, http.StatusOK, accounts.Code)
	assert.Contains(t, accounts.Body.String(), "/redfish/v1/AccountService/Accounts/"+testUsername)

	patch := doRequest(router, http.MethodPatch, "/redfish/v1/AccountService/Accounts/"+testUsername, `{"Password":"site-password"}`)
	assert.Equal(t, http.StatusNoContent, patch.Code, "body=%q", patch.Body.String())

	get := func(user, password string) int {
		req := httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/Bluefield/Oem/Nvidia", nil)
		req.SetBasicAuth(user, password)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		return rec.Code
	}
	assert.Equal(t, http.StatusUnauthorized, get(testUsername, testPassword))
	assert.Equal(t, http.StatusOK, get(testUsername, "site-password"))
}
