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
}

func NewHandler(bmcUser string, bmcPassword string, resourceManager resourcemanager.ResourceManager) *handler {
	return &handler{
		rm:          resourceManager,
		bmcUser:     bmcUser,
		bmcPassword: bmcPassword,
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

func (h *handler) DeleteSession(sessionID string) {
	session.RemoveToken(sessionID)
}

func (h *handler) GetServiceRoot() *server.ServiceRootV1161ServiceRoot {
	return &server.ServiceRootV1161ServiceRoot{
		OdataContext:   "/redfish/v1/$metadata#ServiceRoot.ServiceRoot",
		OdataId:        "/redfish/v1",
		OdataType:      "#ServiceRoot.v1_16_1.ServiceRoot",
		Description:    "ServiceRoot",
		Name:           "ServiceRoot",
		RedfishVersion: "1.16.1",
		UUID:           util.Ptr("00000000-0000-0000-0000-000000000000"),
		Chassis: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Chassis",
		},
		Managers: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Managers",
		},
		Registries: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Registries",
		},
		SessionService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/SessionService",
		},
		Systems: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Systems",
		},
		Tasks: server.OdataV4IdRef{
			OdataId: "/redfish/v1/Tasks",
		},
		AccountService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/AccountService",
		},
		EventService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/EventService",
		},
		TelemetryService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/TelemetryService",
		},
		UpdateService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/UpdateService",
		},
		CompositionService: server.OdataV4IdRef{
			OdataId: "/redfish/v1/CompositionService",
		},
		ProtocolFeaturesSupported: server.ServiceRootV1161ProtocolFeaturesSupported{},
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
