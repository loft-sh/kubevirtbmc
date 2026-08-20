package redfish

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

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
	apiController := server.NewDefaultAPIController(apiService)
	authMiddleware := session.AuthMiddleware(bmcUser, bmcPassword)
	router := server.NewRouter(authMiddleware, apiController)

	// Dell iDRAC attributes are an OEM extension, so they have no generated
	// route. Served only when the BMC claims to be a Dell, so that the vendor
	// it reports and the OEM surface it exposes cannot drift apart.
	// The BIOS settings object has no generated route, and is served whatever
	// vendor is claimed: staging BIOS attributes is standard Redfish, not a
	// Dell extension.
	registerBiosSettingsRoutes(router, authMiddleware, apiService.handler)

	if apiService.handler.identity.isDell() {
		registerDellOemRoutes(router, authMiddleware, resourcemanager.DefaultManagerId)
		registerDellJobRoutes(router, authMiddleware, resourcemanager.DefaultManagerId)
	}

	return &Emulator{
		ctx:         ctx,
		port:        port,
		bmcUser:     bmcUser,
		bmcPassword: bmcPassword,
		server: &http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: canonicalizeTrailingSlash(router),
		},
	}
}

// canonicalizeTrailingSlash serves `/redfish/v1/Foo/` as `/redfish/v1/Foo`
// instead of redirecting to it.
//
// Two problems made this necessary. The route table is built from a map, so
// registration order was random per process start; `/redfish/v1` and
// `/redfish/v1/` are both registered, and under StrictSlash(true) whichever
// landed first served 200 while the other answered 301. Which form was
// canonical therefore flipped from one pod start to the next, and clients ask
// for the trailing-slash form. Sorting the table fixed the randomness, but a
// 301 would still be served for one of the two forms, and for every other
// route asked for with a trailing slash.
//
// That matters because Redfish clients treat a non-2xx as an error rather than
// following it: NICo's preingestion polls `TaskService/Tasks/` with a trailing
// slash as a liveness check and reads any error as "the BMC is not back yet".
// A redirect there is indistinguishable from a dead BMC.
//
// Trimming the slash before matching, rather than turning StrictSlash off,
// means both forms are served directly and no route 404s that used to
// redirect. The trailing slash carries no meaning in Redfish, so normalizing
// it loses nothing. Only Path is touched; the query string is untouched.
func canonicalizeTrailingSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) > 1 && strings.HasSuffix(r.URL.Path, "/") {
			trimmed := r.Clone(r.Context())
			trimmed.URL.Path = strings.TrimRight(r.URL.Path, "/")
			// A path of nothing but slashes would trim to empty and match no
			// route; leave those to the router.
			if trimmed.URL.Path != "" {
				r = trimmed
			}
		}

		next.ServeHTTP(w, r)
	})
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
