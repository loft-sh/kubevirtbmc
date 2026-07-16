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
	authMiddleware := session.AuthMiddleware(bmcUser, bmcPassword)

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

// newRouter builds the Redfish router: the generated routes on an auth-protected
// subrouter plus the DPU-only resources the generated router has no route for.
// Tests build the router through this same function so route reachability matches
// production exactly.
func newRouter(apiService *APIService, authMiddleware mux.MiddlewareFunc) *mux.Router {
	apiController := server.NewDefaultAPIController(apiService)
	router := server.NewRouter(authMiddleware, apiController)

	// The NVIDIA OEM resource carrying the DPU NIC mode and base MAC has no route
	// in the generated Redfish router, so register it directly. It is wrapped in
	// the same auth middleware as the generated protected routes.
	router.
		Methods(http.MethodGet).
		Path("/redfish/v1/Systems/{ComputerSystemId}/Oem/Nvidia").
		Handler(authMiddleware(http.HandlerFunc(apiService.handler.HandleSystemOEM)))

	// The DPU BIOS/boot mutations nico issues while driving a DPU to
	// WaitingForNetworkConfig have no route in the generated router. The shim
	// models no real BIOS and accepts them as no-ops (see HandleDPUConfigNoop).
	router.
		Methods(http.MethodPatch).
		Path("/redfish/v1/Systems/{ComputerSystemId}/Settings").
		Handler(authMiddleware(http.HandlerFunc(apiService.handler.HandleDPUConfigNoop)))
	router.
		Methods(http.MethodPatch).
		Path("/redfish/v1/Systems/{ComputerSystemId}/Bios/Settings").
		Handler(authMiddleware(http.HandlerFunc(apiService.handler.HandleDPUConfigNoop)))
	router.
		Methods(http.MethodPost).
		Path("/redfish/v1/Systems/{ComputerSystemId}/Oem/Nvidia/Actions/HostRshim.Set").
		Handler(authMiddleware(http.HandlerFunc(apiService.handler.HandleDPUConfigNoop)))

	// The HPE BIOS/boot mutations nico issues while driving a host through
	// HostInit use lowercase paths that the generated router has no route for, and
	// libredfish is inconsistent about the trailing slash (e.g. set_virt_enable
	// omits it on Bios/settings, the other writes include it). These are
	// registered on a subrouter with StrictSlash disabled so the optional trailing
	// slash is matched by the route regex ({_:/?}) directly; under the router's
	// StrictSlash(true) the opposite slash form would 301, and a redirected PATCH
	// is dropped to a GET per HTTP redirect rules. The shim accepts the writes as
	// no-ops and serves the proprietary boot-order resource. A host BMC serves
	// these; the handlers 404 for a DPU.
	hostRoutes := router.NewRoute().Subrouter()
	hostRoutes.StrictSlash(false)
	hostRoutes.
		Methods(http.MethodGet).
		Path("/redfish/v1/Systems/{ComputerSystemId}/Bios/oem/hpe/boot{_:/?}").
		Handler(authMiddleware(http.HandlerFunc(apiService.handler.HandleHostBiosOemBoot)))
	hostRoutes.
		Methods(http.MethodPatch).
		Path("/redfish/v1/Systems/{ComputerSystemId}/Bios/oem/hpe/boot/settings{_:/?}").
		Handler(authMiddleware(http.HandlerFunc(apiService.handler.HandleHostConfigNoop)))
	hostRoutes.
		Methods(http.MethodPatch).
		Path("/redfish/v1/Systems/{ComputerSystemId}/Bios/settings{_:/?}").
		Handler(authMiddleware(http.HandlerFunc(apiService.handler.HandleHostConfigNoop)))

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
