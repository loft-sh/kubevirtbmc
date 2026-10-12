package redfish

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

type Emulator struct {
	ctx  context.Context
	port int

	bmcUser     string
	bmcPassword string

	wg     sync.WaitGroup
	server *http.Server
}

func NewEmulator(ctx context.Context, port int, bmcUser string, bmcPassword string, resourceManager resourcemanager.ResourceManager) *Emulator {
	apiService := NewAPIService(bmcUser, bmcPassword, resourceManager)
	authMiddleware := session.AuthMiddleware(apiService.handler.VerifyCredentials)

	return &Emulator{
		ctx:         ctx,
		port:        port,
		bmcUser:     bmcUser,
		bmcPassword: bmcPassword,
		server: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: newRouter(apiService, authMiddleware),
		},
	}
}

// newRouter builds the Redfish router. The routes below are registered ahead of
// the generated ones, so they also take precedence over a generated route for
// the same path: the generated stubs for AccountService and SecureBoot return
// 501 or decode into typed bodies that reject partial PATCHes, and the vendor
// resources have no generated route at all. Tests build the router through this
// same function so route reachability matches production exactly.
func newRouter(apiService *APIService, authMiddleware mux.MiddlewareFunc) *mux.Router {
	h := apiService.handler

	// The HPE host routes use lowercase paths, and libredfish is inconsistent about
	// their trailing slash (e.g. set_virt_enable omits it on Bios/settings, the
	// other writes include it), so the optional slash is part of the route
	// ({_:/?}): a redirect would turn the PATCH into a GET.
	routes := []struct {
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{http.MethodGet, accountServicePath, h.HandleAccountService},
		{http.MethodPatch, accountServicePath, h.HandleAccountServicePatch},
		{http.MethodGet, "/redfish/v1/AccountService/Accounts", h.HandleAccounts},
		{http.MethodGet, "/redfish/v1/AccountService/Accounts/{AccountId}", h.HandleAccount},
		{http.MethodPatch, "/redfish/v1/AccountService/Accounts/{AccountId}", h.HandleAccountPatch},

		// DPU
		{http.MethodGet, "/redfish/v1/Systems/{ComputerSystemId}/Oem/Nvidia", h.HandleSystemOEM},
		{http.MethodPatch, "/redfish/v1/Managers/{ManagerId}/Oem/Nvidia", h.HandleDPUConfigNoop},
		{http.MethodPost, "/redfish/v1/Systems/{ComputerSystemId}/Oem/Nvidia/Actions/HostRshim.Set", h.HandleHostRshimSet},
		{http.MethodPost, "/redfish/v1/Systems/{ComputerSystemId}/Oem/Nvidia/Actions/Mode.Set", h.HandleModeSet},
		{http.MethodPatch, "/redfish/v1/Systems/{ComputerSystemId}/Settings", h.HandleSystemSettings},
		{http.MethodPatch, "/redfish/v1/Systems/{ComputerSystemId}/Bios/Settings", h.HandleDPUConfigNoop},
		{http.MethodPatch, "/redfish/v1/Systems/{ComputerSystemId}/SecureBoot", h.HandleSecureBootPatch},
		{http.MethodPost, "/redfish/v1/Systems/{ComputerSystemId}/SecureBoot/SecureBootDatabases/{DatabaseId}/Certificates", h.HandleSecureBootCertificateAdd},

		// Host
		{http.MethodGet, "/redfish/v1/Systems/{ComputerSystemId}/Bios/oem/hpe/boot{_:/?}", h.HandleHostBiosOemBoot},
		{http.MethodPatch, "/redfish/v1/Systems/{ComputerSystemId}/Bios/oem/hpe/boot/settings{_:/?}", h.HandleHostConfigNoop},
		{http.MethodPatch, "/redfish/v1/Systems/{ComputerSystemId}/Bios/settings{_:/?}", h.HandleHostConfigNoop},
	}

	router := mux.NewRouter()
	for _, route := range routes {
		router.
			Methods(route.method).
			Path(route.path).
			Handler(authMiddleware(route.handler))
	}

	router.PathPrefix("/").Handler(server.NewRouter(authMiddleware, server.NewDefaultAPIController(apiService)))

	return router
}

func (e *Emulator) Run() error {
	e.wg.Add(1)

	go func() {
		defer e.wg.Done()

		if err := e.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Println(err)
		}
	}()

	return nil
}

func (e *Emulator) Stop() {
	if err := e.server.Shutdown(e.ctx); err != nil {
		fmt.Println(err)
	}
	e.wg.Wait()
	logrus.Info("Redfish emulator gracefully stopped")
}
