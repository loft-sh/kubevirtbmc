package redfish

import (
	"fmt"

	"github.com/google/uuid"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

type handler struct {
	rm resourcemanager.ResourceManager

	bmcUser     string
	bmcPassword string

	// identity is resolved once, here, rather than on every read of
	// ServiceRoot: it comes from the environment and cannot change without a
	// restart.
	identity serviceRootIdentity

	// networkProtocol is writable, so it holds state for the lifetime of the
	// handler rather than being rebuilt per read.
	networkProtocol *networkProtocolState
}

func NewHandler(bmcUser string, bmcPassword string, resourceManager resourcemanager.ResourceManager) *handler {
	identity := identityFromEnv()
	identity.log()

	return &handler{
		rm:              resourceManager,
		bmcUser:         bmcUser,
		bmcPassword:     bmcPassword,
		identity:        identity,
		networkProtocol: newNetworkProtocolState(),
	}
}

func (h *handler) Authenticate(username, password *string) (string, string, error) {
	var id, token string
	if username == nil || password == nil {
		return id, token, fmt.Errorf("username and password must be provided")
	}

	if *username != h.bmcUser || *password != h.bmcPassword {
		return id, token, fmt.Errorf("invalid username or password")
	}

	id = uuid.New().String()
	tokenInfo := session.NewTokenInfo(id, *username)
	token = session.AddToken(tokenInfo)

	return id, token, nil
}

func (h *handler) GetSession(sessionID string) (string, string, error) {
	var id, username string
	tokenInfo, exists := session.GetTokenFromSessionID(sessionID)
	if !exists {
		return id, username, fmt.Errorf("session not found")
	}
	return tokenInfo.ID, tokenInfo.Username, nil
}

// DeleteSession revokes a session by its ID and reports whether it existed.
// The token store is keyed by token, so this needs RemoveSession's reverse
// lookup: RemoveToken takes a token, and handing it a session ID deletes
// nothing. Clients that rotate credentials (NICo) DELETE the session they
// previously created, and a 204 that revoked nothing would leave the session
// valid and the Sessions collection growing without bound.
func (h *handler) DeleteSession(sessionID string) bool {
	return session.RemoveSession(sessionID)
}

// GetSessionService serves /redfish/v1/SessionService.
//
// ServiceRoot advertises this resource, and "advertised but 501" is the one
// shape a Redfish client cannot recover from. nv-redfish (which NICo's
// nico-hardware-health and nico-bmc-proxy reach BMCs through) treats a missing
// nav property in ServiceRoot as "no session service" and falls back to Basic
// auth, but treats an error on a nav property that *is* advertised as a hard
// failure with no fallback. virtbmc has always implemented session creation,
// so the service is real; only its discovery document was missing.
//
// The Sessions link is mandatory rather than decorative: a SessionService
// without it makes the client fail with "does not expose a Sessions
// collection", which is also outside the fallback path.
func (h *handler) GetSessionService() *server.SessionServiceV118SessionService {
	return &server.SessionServiceV118SessionService{
		OdataContext:   "/redfish/v1/$metadata#SessionService.SessionService",
		OdataId:        "/redfish/v1/SessionService",
		OdataType:      "#SessionService.v1_1_8.SessionService",
		Id:             "SessionService",
		Name:           "Session Service",
		Description:    "Session Service",
		ServiceEnabled: util.Ptr(true),
		// virtbmc never expires a session, and Redfish has no value for
		// "never", so this advertises the schema maximum (24h) rather than a
		// timeout that would be enforced. Nothing in nv-redfish or NICo reads
		// it; it is here because the field is part of the resource clients
		// expect.
		SessionTimeout: 86400,
		Sessions: server.OdataV4IdRef{
			OdataId: "/redfish/v1/SessionService/Sessions",
		},
		Status: server.ResourceStatus{
			Health: util.Ptr(server.RESOURCEHEALTH_OK),
			State:  util.Ptr(server.RESOURCESTATE_ENABLED),
		},
	}
}

// GetSessionCollection serves /redfish/v1/SessionService/Sessions.
//
// This is the second half of the SessionService fix, not an optional extra:
// nv-redfish GETs this collection immediately after the SessionService
// document, so leaving it at 501 moves the unrecoverable failure one hop down
// rather than fixing it.
//
// Members are the live sessions, so a client that created a session can find
// it again and revoke it.
func (h *handler) GetSessionCollection() *server.SessionCollectionSessionCollection {
	sessions := session.ListSessions()

	members := make([]server.OdataV4IdRef, 0, len(sessions))
	for _, s := range sessions {
		members = append(members, server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/SessionService/Sessions/%s", s.ID),
		})
	}

	return &server.SessionCollectionSessionCollection{
		OdataContext:      "/redfish/v1/$metadata#SessionCollection.SessionCollection",
		OdataId:           "/redfish/v1/SessionService/Sessions",
		OdataType:         "#SessionCollection.SessionCollection",
		Name:              "Session Collection",
		Description:       "Session Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}
}

func (h *handler) GetServiceRoot() *server.ServiceRootV1161ServiceRoot {
	serviceRoot := &server.ServiceRootV1161ServiceRoot{
		OdataContext: "/redfish/v1/$metadata#ServiceRoot.ServiceRoot",
		OdataId:      "/redfish/v1",
		OdataType:    "#ServiceRoot.v1_16_1.ServiceRoot",
		// Id is required and carries no omitempty, so leaving it unset shipped
		// `"Id":""`. RootService is the value the DMTF ServiceRoot mockups and
		// shipping BMCs use.
		Id:             "RootService",
		Description:    "ServiceRoot",
		Name:           "ServiceRoot",
		RedfishVersion: "1.16.1",
		UUID:           util.Ptr("00000000-0000-0000-0000-000000000000"),
		// Redfish makes Vendor optional, but clients gate on it: NICo's
		// site-explorer refuses a ServiceRoot that reports no recognized
		// vendor, and it reads Vendor first, falling back to the first Oem key.
		// Reporting Vendor is the direct answer, so Oem is left alone rather
		// than made into a second, potentially conflicting signal.
		Vendor: util.Ptr(h.identity.vendor),
		Chassis: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Chassis",
		},
		Managers: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Managers",
		},
		SessionService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/SessionService",
		},
		Systems: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Systems",
		},
		// Points at the TaskService resource, which is served. It previously
		// pointed at /redfish/v1/Tasks, for which no route exists at all.
		Tasks: server.OdataV4IdRef{
			OdataId: "/redfish/v1/TaskService",
		},
		UpdateService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/UpdateService",
		},
		// Registries, AccountService, EventService, TelemetryService and
		// CompositionService are deliberately not advertised. Every one of them
		// answered 501, and an advertised link that fails is worse than no
		// link: a client that walks the service root treats the error as a
		// broken BMC, whereas an absent property means "not supported", which
		// is both true and what Redfish says to do.
		//
		// ProtocolFeaturesSupported is likewise left unset. Absent means the
		// client does not try $expand and fetches collection members
		// individually, which is what this service can actually serve.
		Links: server.ServiceRootV1161Links{
			ManagerProvidingService: server.OdataV4IdRef{
				OdataId: "/redfish/v1/Managers/BMC",
			},
			Oem: map[string]interface{}{},
			Sessions: server.OdataV4IdRef{
				OdataId: "/redfish/v1/SessionService/Sessions",
			},
		},
	}

	// Left absent unless configured. Redfish reads an absent property as "not
	// reported"; an empty string would instead assert a product whose name is
	// blank.
	if h.identity.product != "" {
		serviceRoot.Product = util.Ptr(h.identity.product)
	}

	return serviceRoot
}

func (h *handler) GetManagerCollection() *server.ManagerCollectionManagerCollection {
	members := []server.OdataV4IdRef{
		{
			OdataId: "/redfish/v1/Managers/BMC",
		},
	}

	return &server.ManagerCollectionManagerCollection{
		OdataContext:      "/redfish/v1/$metadata#ManagerCollection.ManagerCollection",
		OdataId:           "/redfish/v1/Managers",
		OdataType:         "#ManagerCollection.ManagerCollection",
		Description:       "Manager Collection",
		Name:              "Manager Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}
}

func (h *handler) GetManager() (*server.ManagerV1190Manager, error) {
	manager, err := h.rm.GetManager()
	if err != nil {
		return nil, err
	}

	adapter, ok := manager.(*resourcemanager.ManagerAdapter)
	if !ok {
		return nil, fmt.Errorf("manager is not a *resourcemanager.ManagerAdapter (got %T)", manager)
	}

	return adapter.Manager(), nil
}

func (h *handler) GetVirtualMediaCollection() *server.VirtualMediaCollectionVirtualMediaCollection {
	return &server.VirtualMediaCollectionVirtualMediaCollection{
		OdataContext: "/redfish/v1/$metadata#VirtualMediaCollection.VirtualMediaCollection",
		OdataId:      "/redfish/v1/Managers/BMC/VirtualMedia",
		OdataType:    "#VirtualMediaCollection.VirtualMediaCollection",
		Description:  "Virtual Media Collection",
		Name:         "Virtual Media Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: "/redfish/v1/Managers/BMC/VirtualMedia/CD1",
			},
		},
		MembersodataCount: 1,
	}
}

func (h *handler) GetVirtualMedia() (*server.VirtualMediaV163VirtualMedia, error) {
	virtualMedia, err := h.rm.GetVirtualMedia()
	if err != nil {
		return nil, err
	}

	adapter, ok := virtualMedia.(*resourcemanager.VirtualMediaAdapter)
	if !ok {
		return nil, fmt.Errorf("virtualMedia is not a *resourcemanager.VirtualMediaAdapter (got %T)", virtualMedia)
	}

	return adapter.VirtualMedia(), nil
}

func (h *handler) VirtualMediaEject() error {
	return h.rm.EjectMedia()
}

func (h *handler) VirtualMediaInsert(image string) error {
	return h.rm.InsertMedia(image)
}

func (h *handler) GetComputerSystemCollection() *server.ComputerSystemCollectionComputerSystemCollection {
	members := []server.OdataV4IdRef{
		{
			OdataId: "/redfish/v1/Systems/1",
		},
	}

	return &server.ComputerSystemCollectionComputerSystemCollection{
		OdataContext:      "/redfish/v1/$metadata#ComputerSystemCollection.ComputerSystemCollection",
		OdataId:           "/redfish/v1/Systems",
		OdataType:         "#ComputerSystemCollection.ComputerSystemCollection",
		Description:       "Computer System Collection",
		Name:              "Computer System Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}
}

func (h *handler) GetComputerSystem() (*server.ComputerSystemV1220ComputerSystem, error) {
	computerSystem, err := h.rm.GetComputerSystem()
	if err != nil {
		return nil, err
	}

	adapter, ok := computerSystem.(*resourcemanager.ComputerSystemAdapter)
	if !ok {
		return nil, fmt.Errorf("computerSystem is not a *resourcemanager.ComputerSystemAdapter (got %T)", computerSystem)
	}

	return adapter.ComputerSystem(), nil
}

func (h *handler) PatchComputerSystem(computerSystemPatch *server.ComputerSystemV1220ComputerSystem) error {
	boot := computerSystemPatch.Boot
	if boot.BootSourceOverrideEnabled != server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED {
		var bootDevice resourcemanager.BootDevice

		switch boot.BootSourceOverrideTarget {
		case server.COMPUTERSYSTEMBOOTSOURCE_PXE:
			bootDevice = resourcemanager.BootDevicePxe
		case server.COMPUTERSYSTEMBOOTSOURCE_HDD:
			bootDevice = resourcemanager.BootDeviceHdd
		case server.COMPUTERSYSTEMBOOTSOURCE_CD:
			bootDevice = resourcemanager.BootDeviceCd
		default:
			return nil
		}

		if err := h.rm.SetBootDevice(bootDevice); err != nil {
			return err
		}
	}
	return nil
}

func (h *handler) ComputerSystemReset(resetType server.ResourceResetType) error {
	powerActionMap := map[server.ResourceResetType]func() error{
		server.RESOURCERESETTYPE_ON:                h.rm.PowerOn,
		server.RESOURCERESETTYPE_GRACEFUL_SHUTDOWN: h.rm.PowerOff,
		server.RESOURCERESETTYPE_FORCE_OFF:         h.rm.PowerOff,
		server.RESOURCERESETTYPE_GRACEFUL_RESTART:  h.rm.PowerCycle,
		server.RESOURCERESETTYPE_FORCE_RESTART:     h.rm.PowerCycle,
	}

	powerAction, ok := powerActionMap[resetType]
	if !ok {
		return fmt.Errorf("unsupported reset type: %s", resetType)
	}
	return powerAction()
}

// ComputerSystemSetDefaultBootOrder sets the boot order for the computer system back to default.
// TODO: Implement real default boot order setting. Right now we intentionally misuse the handler to set the first boot
// device.
func (h *handler) ComputerSystemSetDefaultBootOrder(bootDevices []string) error {
	var bootDevice resourcemanager.BootDevice
	if len(bootDevices) > 0 {
		bootDevice = resourcemanager.BootDevice(bootDevices[0])
	}
	return h.rm.SetBootDevice(bootDevice)
}

func (h *handler) GetEthernetInterfaceCollection() (*server.EthernetInterfaceCollectionEthernetInterfaceCollection, error) {
	interfaces, err := h.rm.GetEthernetInterfaces()
	if err != nil {
		return nil, err
	}

	members := make([]server.OdataV4IdRef, 0, len(interfaces))
	for _, iface := range interfaces {
		members = append(members, server.OdataV4IdRef{
			OdataId: iface.OdataId(),
		})
	}

	return &server.EthernetInterfaceCollectionEthernetInterfaceCollection{
		OdataContext:      "/redfish/v1/$metadata#EthernetInterfaceCollection.EthernetInterfaceCollection",
		OdataId:           "/redfish/v1/Systems/1/EthernetInterfaces",
		OdataType:         "#EthernetInterfaceCollection.EthernetInterfaceCollection",
		Name:              "Ethernet Interface Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}, nil
}

func (h *handler) GetEthernetInterface(interfaceId string) (*server.EthernetInterfaceV1120EthernetInterface, error) {
	interfaces, err := h.rm.GetEthernetInterfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range interfaces {
		if iface.Id() == interfaceId {
			adapter, ok := iface.(*resourcemanager.EthernetInterfaceAdapter)
			if !ok {
				return nil, fmt.Errorf("ethernetInterface is not a *resourcemanager.EthernetInterfaceAdapter (got %T)", iface)
			}
			return adapter.EthernetInterface(), nil
		}
	}

	return nil, fmt.Errorf("ethernet interface not found: %s", interfaceId)
}

// GetChassisCollection returns an empty Chassis collection.
//
// ServiceRoot advertises /redfish/v1/Chassis, so a client that walks the
// service tree will follow that link. Returning 501 there is fatal for clients
// that treat "not implemented" as a hard error rather than as "unsupported"
// (libredfish does exactly this). virtbmc fronts a VM, which has no chassis to
// describe, so the honest answer is an empty collection rather than an error.
func (h *handler) GetChassisCollection() *server.ChassisCollectionChassisCollection {
	return &server.ChassisCollectionChassisCollection{
		OdataContext:      "/redfish/v1/$metadata#ChassisCollection.ChassisCollection",
		OdataId:           "/redfish/v1/Chassis",
		OdataType:         "#ChassisCollection.ChassisCollection",
		Name:              "Chassis Collection",
		Description:       "Chassis Collection",
		Members:           []server.OdataV4IdRef{},
		MembersodataCount: 0,
	}
}

// GetUpdateService returns a disabled UpdateService.
//
// virtbmc cannot flash firmware, but clients reach FirmwareInventory *through*
// this resource, so leaving it at 501 makes the inventory below it
// unreachable regardless of whether that is implemented. ServiceEnabled=false
// tells the client the truth without erroring.
func (h *handler) GetUpdateService() *server.UpdateServiceV1130UpdateService {
	return &server.UpdateServiceV1130UpdateService{
		OdataContext:   "/redfish/v1/$metadata#UpdateService.UpdateService",
		OdataId:        "/redfish/v1/UpdateService",
		OdataType:      "#UpdateService.v1_13_0.UpdateService",
		Name:           "Update Service",
		Description:    "Update Service",
		Id:             "UpdateService",
		ServiceEnabled: util.Ptr(false),
		FirmwareInventory: server.OdataV4IdRef{
			OdataId: "/redfish/v1/UpdateService/FirmwareInventory",
		},
		Status: server.ResourceStatus{
			Health: util.Ptr(server.RESOURCEHEALTH_OK),
			State:  util.Ptr(server.RESOURCESTATE_DISABLED),
		},
	}
}

// GetFirmwareInventory returns an empty SoftwareInventory collection. There is
// no firmware behind a VM to enumerate; see GetChassisCollection for why this
// is an empty 200 rather than a 501.
func (h *handler) GetFirmwareInventory() *server.SoftwareInventoryCollectionSoftwareInventoryCollection {
	return &server.SoftwareInventoryCollectionSoftwareInventoryCollection{
		OdataContext:      "/redfish/v1/$metadata#SoftwareInventoryCollection.SoftwareInventoryCollection",
		OdataId:           "/redfish/v1/UpdateService/FirmwareInventory",
		OdataType:         "#SoftwareInventoryCollection.SoftwareInventoryCollection",
		Name:              "Firmware Inventory Collection",
		Description:       "Firmware Inventory Collection",
		Members:           []server.OdataV4IdRef{},
		MembersodataCount: 0,
	}
}

// GetManagerEthernetInterfaceCollection serves the *Manager*-anchored NIC
// collection at /redfish/v1/Managers/{id}/EthernetInterfaces.
//
// This is a different resource from the ComputerSystem-anchored one above, and
// the Manager resource has always advertised a link to it. Clients that want
// the BMC's own MAC (rather than the host's) walk this path, so implementing
// only the System path leaves them with nothing. The underlying data is the
// same set of VM interfaces; only the anchoring OdataId differs.
func (h *handler) GetManagerEthernetInterfaceCollection(managerID string) (*server.EthernetInterfaceCollectionEthernetInterfaceCollection, error) {
	interfaces, err := h.rm.GetEthernetInterfaces()
	if err != nil {
		return nil, err
	}

	base := fmt.Sprintf("/redfish/v1/Managers/%s/EthernetInterfaces", managerID)
	members := make([]server.OdataV4IdRef, 0, len(interfaces))
	for _, iface := range interfaces {
		members = append(members, server.OdataV4IdRef{
			OdataId: fmt.Sprintf("%s/%s", base, iface.Id()),
		})
	}

	return &server.EthernetInterfaceCollectionEthernetInterfaceCollection{
		OdataContext:      "/redfish/v1/$metadata#EthernetInterfaceCollection.EthernetInterfaceCollection",
		OdataId:           base,
		OdataType:         "#EthernetInterfaceCollection.EthernetInterfaceCollection",
		Name:              "Ethernet Interface Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}, nil
}

// GetManagerEthernetInterface serves a single Manager-anchored NIC. It rebuilds
// the resource under the Manager OdataId so the document a client fetches is
// self-consistent with the URL it fetched it from.
func (h *handler) GetManagerEthernetInterface(managerID, interfaceID string) (*server.EthernetInterfaceV1120EthernetInterface, error) {
	interfaces, err := h.rm.GetEthernetInterfaces()
	if err != nil {
		return nil, err
	}

	for _, iface := range interfaces {
		if iface.Id() != interfaceID {
			continue
		}
		adapter, ok := iface.(*resourcemanager.EthernetInterfaceAdapter)
		if !ok {
			return nil, fmt.Errorf("ethernetInterface is not a *resourcemanager.EthernetInterfaceAdapter (got %T)", iface)
		}
		source := adapter.EthernetInterface()
		enabled := false
		if source.InterfaceEnabled != nil {
			enabled = *source.InterfaceEnabled
		}
		return resourcemanager.NewManagerEthernetInterface(
			managerID,
			source.Id,
			source.Name,
			source.MACAddress,
			enabled,
			source.LinkStatus,
		).EthernetInterface(), nil
	}

	return nil, fmt.Errorf("ethernet interface not found: %s", interfaceID)
}

// GetTaskService serves the task service.
//
// virtbmc runs no long-running Redfish tasks, but the service and its
// collection still have to exist. A client that resets the BMC polls
// TaskService/Tasks as a liveness check and treats any error, a 404 included,
// as "the BMC is not back yet", so a missing collection leaves the host
// powered off indefinitely rather than merely looking untidy.
func (h *handler) GetTaskService() *server.TaskServiceV120TaskService {
	return &server.TaskServiceV120TaskService{
		OdataContext:   "/redfish/v1/$metadata#TaskService.TaskService",
		OdataId:        "/redfish/v1/TaskService",
		OdataType:      "#TaskService.v1_2_0.TaskService",
		Id:             "TaskService",
		Name:           "Task Service",
		Description:    "Task Service",
		ServiceEnabled: util.Ptr(true),
		Status: server.ResourceStatus{
			Health: util.Ptr(server.RESOURCEHEALTH_OK),
			State:  util.Ptr(server.RESOURCESTATE_ENABLED),
		},
		Tasks: server.OdataV4IdRef{
			OdataId: "/redfish/v1/TaskService/Tasks",
		},
	}
}

// GetTaskCollection serves an empty task collection. Members and the count
// must both be present: "no tasks" is a real answer and has to be
// distinguishable from a broken endpoint.
func (h *handler) GetTaskCollection() *server.TaskCollectionTaskCollection {
	return &server.TaskCollectionTaskCollection{
		OdataContext:      "/redfish/v1/$metadata#TaskCollection.TaskCollection",
		OdataId:           "/redfish/v1/TaskService/Tasks",
		OdataType:         "#TaskCollection.TaskCollection",
		Name:              "Task Collection",
		Description:       "Task Collection",
		Members:           []server.OdataV4IdRef{},
		MembersodataCount: 0,
	}
}

// GetStorageCollection serves an empty storage collection for a system.
//
// A Dell host gets looked up here for a BOSS controller. virtbmc has no
// storage controllers to report, but the collection must exist: NVIDIA's own
// mock adds it for exactly this reason, to avoid the 404.
func (h *handler) GetStorageCollection(computerSystemID string) *server.StorageCollectionStorageCollection {
	return &server.StorageCollectionStorageCollection{
		OdataContext:      "/redfish/v1/$metadata#StorageCollection.StorageCollection",
		OdataId:           fmt.Sprintf("/redfish/v1/Systems/%s/Storage", computerSystemID),
		OdataType:         "#StorageCollection.StorageCollection",
		Name:              "Storage Collection",
		Description:       "Storage Collection",
		Members:           []server.OdataV4IdRef{},
		MembersodataCount: 0,
	}
}
