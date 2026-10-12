package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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

// VerifyCredentials checks the basic auth credentials. Once a client has changed
// the password through AccountService, only the new one is accepted; until then
// it is the password the BMC was started with.
func (h *handler) VerifyCredentials(username, password string) bool {
	if username != h.bmcUser {
		return false
	}

	if hash := h.rm.PasswordHash(); hash != "" {
		return session.VerifyPassword(hash, password)
	}

	return password == h.bmcPassword
}

func (h *handler) Authenticate(username, password *string) (string, string, error) {
	var id, token string
	if username == nil || password == nil {
		return id, token, fmt.Errorf("username and password must be provided")
	}

	if !h.VerifyCredentials(*username, *password) {
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

// GetServiceRoot reports the vendor and product from the identity. NVIDIA's
// libredfish client selects its vendor client from them: only the BlueField one
// reads NIC mode and base MAC from Systems/<id>/Oem/Nvidia, and an unrecognized
// vendor skips its init path, so it never discovers the system and manager ids.
// NICo's nv-redfish explorer additionally requires vendor "Nvidia" together with
// system id "Bluefield" to classify a BMC as a BlueField, and skips the
// Bluefield_ERoT chassis for product "BlueField-3 DPU".
func (h *handler) GetServiceRoot() *server.ServiceRootV1161ServiceRoot {
	id := h.rm.Identity()

	root := &server.ServiceRootV1161ServiceRoot{
		OdataContext:   "/redfish/v1/$metadata#ServiceRoot.ServiceRoot",
		OdataId:        "/redfish/v1",
		OdataType:      "#ServiceRoot.v1_16_1.ServiceRoot",
		Description:    "ServiceRoot",
		Name:           "ServiceRoot",
		RedfishVersion: "1.16.1",
		Vendor:         util.Ptr(id.Vendor),
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
			OdataId: accountServicePath,
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
				OdataId: fmt.Sprintf("/redfish/v1/Managers/%s", id.ManagerID),
			},
			Oem: map[string]interface{}{},
			Sessions: server.OdataV4IdRef{
				OdataId: "/redfish/v1/SessionService/Sessions",
			},
		},
	}
	if id.Product != "" {
		root.Product = util.Ptr(id.Product)
	}

	return root
}

func (h *handler) GetManagerCollection() *server.ManagerCollectionManagerCollection {
	return &server.ManagerCollectionManagerCollection{
		OdataContext: "/redfish/v1/$metadata#ManagerCollection.ManagerCollection",
		OdataId:      "/redfish/v1/Managers",
		OdataType:    "#ManagerCollection.ManagerCollection",
		Description:  "Manager Collection",
		Name:         "Manager Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: fmt.Sprintf("/redfish/v1/Managers/%s", h.rm.Identity().ManagerID),
			},
		},
		MembersodataCount: 1,
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

	mgr := adapter.Manager()

	// DateTime is computed per request so nico's preingestion clock-drift gate
	// (hard ±300s) always sees a current time, regardless of BMC pod age.
	mgr.DateTime = util.Ptr(time.Now())

	// A DPU reports its BMC firmware version.
	//
	// A host reports the iLO firmware version and Oem.Hpe.VirtualNICEnabled that
	// nico's HPE client reads: lockdown_status treats the BMC as "fully disabled"
	// only when VirtualNICEnabled is true (together with Bios UsbBoot "Enabled"),
	// which keeps HostInit out of the WaitingForLockdown detour.
	id := h.rm.Identity()
	if id.DPU {
		mgr.FirmwareVersion = util.Ptr(id.BMCFirmware)
		return mgr, nil
	}

	mgr.FirmwareVersion = util.Ptr(hostManagerFirmwareVersion)
	mgr.Oem = map[string]interface{}{
		"Hpe": map[string]interface{}{
			"VirtualNICEnabled": true,
		},
	}

	return mgr, nil
}

func (h *handler) GetVirtualMediaCollection() *server.VirtualMediaCollectionVirtualMediaCollection {
	managerID := h.rm.Identity().ManagerID

	return &server.VirtualMediaCollectionVirtualMediaCollection{
		OdataContext: "/redfish/v1/$metadata#VirtualMediaCollection.VirtualMediaCollection",
		OdataId:      fmt.Sprintf("/redfish/v1/Managers/%s/VirtualMedia", managerID),
		OdataType:    "#VirtualMediaCollection.VirtualMediaCollection",
		Description:  "Virtual Media Collection",
		Name:         "Virtual Media Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/VirtualMedia/CD1", managerID),
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
	return &server.ComputerSystemCollectionComputerSystemCollection{
		OdataContext: "/redfish/v1/$metadata#ComputerSystemCollection.ComputerSystemCollection",
		OdataId:      "/redfish/v1/Systems",
		OdataType:    "#ComputerSystemCollection.ComputerSystemCollection",
		Description:  "Computer System Collection",
		Name:         "Computer System Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: fmt.Sprintf("/redfish/v1/Systems/%s", h.rm.Identity().SystemID),
			},
		},
		MembersodataCount: 1,
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

	cs := adapter.ComputerSystem()

	// A host advertises its boot order so nico's CheckBootOrder gate passes:
	// BootOrder[0] must equal the BootOptionReference of the HTTP/IPv4 boot option
	// for the boot NIC. The reorder nico issues (PATCH Systems/1) is a no-op here,
	// so BootOrder[0] must already be that reference.
	id := h.rm.Identity()
	if !id.DPU && id.HostBootMAC != "" {
		cs.Boot.BootOrder = []*string{util.Ptr(hostBootOptionReference)}
	}

	return cs, nil
}

func (h *handler) PatchComputerSystem(computerSystemPatch *server.ComputerSystemV1220ComputerSystem) error {
	boot := computerSystemPatch.Boot

	return h.setBootOverride(boot.BootSourceOverrideEnabled, boot.BootSourceOverrideTarget)
}

// setBootOverride applies a Redfish boot source override. Once arms a one-time
// boot that the next restart consumes, Disabled cancels it, and anything else is
// a persistent boot device.
func (h *handler) setBootOverride(enabled server.ComputerSystemV1220BootSourceOverrideEnabled, target server.ComputerSystemBootSource) error {
	switch enabled {
	case server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE:
		return h.rm.SetBootOnce(target)
	case server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED:
		return h.rm.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_NONE)
	}

	var bootDevice resourcemanager.BootDevice
	switch target {
	case server.COMPUTERSYSTEMBOOTSOURCE_PXE:
		bootDevice = resourcemanager.BootDevicePxe
	case server.COMPUTERSYSTEMBOOTSOURCE_HDD:
		bootDevice = resourcemanager.BootDeviceHdd
	case server.COMPUTERSYSTEMBOOTSOURCE_CD:
		bootDevice = resourcemanager.BootDeviceCd
	default:
		return nil
	}

	return h.rm.SetBootDevice(bootDevice)
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
		OdataId:           fmt.Sprintf("/redfish/v1/Systems/%s/EthernetInterfaces", h.rm.Identity().SystemID),
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

// dpuPCIeDeviceID is the single PCIe device a host BMC reports: the attached
// DPU. Its serial number is what NICo matches against the DPU's system serial.
const dpuPCIeDeviceID = "DPU"

// defaultChassisId is the enclosure every BMC serves; a DPU additionally serves
// the dpuCardChassisId ("Card1") chassis NVIDIA's clients read the BlueField
// model and part number from.
const (
	defaultChassisId = "1"
	dpuCardChassisId = "Card1"
)

func (h *handler) GetChassisCollection() *server.ChassisCollectionChassisCollection {
	members := []server.OdataV4IdRef{
		{
			OdataId: fmt.Sprintf("/redfish/v1/Chassis/%s", defaultChassisId),
		},
	}

	if h.rm.Identity().DPU {
		members = append(members, server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Chassis/%s", dpuCardChassisId),
		})
	}

	return &server.ChassisCollectionChassisCollection{
		OdataContext:      "/redfish/v1/$metadata#ChassisCollection.ChassisCollection",
		OdataId:           "/redfish/v1/Chassis",
		OdataType:         "#ChassisCollection.ChassisCollection",
		Description:       "Chassis Collection",
		Name:              "Chassis Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}
}

// GetChassis returns a minimal enclosure. For the default chassis its id matches
// the system id so the standard Redfish client's default PCIe lookup
// (Chassis/{system_id}/PCIeDevices) resolves; on a host with an attached DPU the
// PCIeDevices reference is set so that lookup is followed. For a DPU, the "Card1"
// chassis carries the BlueField model, part number and serial NVIDIA's clients
// classify on. References left unset are dropped by sanitize so clients see them
// as absent rather than an empty reference missing its required @odata.id.
func (h *handler) GetChassis(chassisID string) *server.ChassisV1250Chassis {
	id := h.rm.Identity()

	if id.DPU && chassisID == dpuCardChassisId {
		return &server.ChassisV1250Chassis{
			OdataContext: "/redfish/v1/$metadata#Chassis.Chassis",
			OdataId:      fmt.Sprintf("/redfish/v1/Chassis/%s", chassisID),
			OdataType:    "#Chassis.v1_25_0.Chassis",
			Id:           chassisID,
			Name:         "Network Adapter Card",
			ChassisType:  server.CHASSISV1250CHASSISTYPE_CARD,
			Manufacturer: util.Ptr(id.Manufacturer),
			Model:        util.Ptr(id.DPUModel),
			PartNumber:   util.Ptr(id.PartNumber),
			SerialNumber: util.Ptr(id.SystemSerial),
		}
	}

	chassis := server.ChassisV1250Chassis{
		OdataContext: "/redfish/v1/$metadata#Chassis.Chassis",
		OdataId:      fmt.Sprintf("/redfish/v1/Chassis/%s", chassisID),
		OdataType:    "#Chassis.v1_25_0.Chassis",
		Id:           chassisID,
		Name:         "Computer System Chassis",
		ChassisType:  server.CHASSISV1250CHASSISTYPE_RACK_MOUNT,
		Manufacturer: util.Ptr(id.Manufacturer),
	}

	// A host with an attached DPU points at its PCIe inventory so the Redfish
	// client follows the link and reads the DPU device beneath it.
	if id.PCIeDeviceSerial != "" {
		chassis.PCIeDevices = server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Chassis/%s/PCIeDevices", chassisID),
		}
	}

	return &chassis
}

func (h *handler) GetPCIeDeviceCollection(chassisID string) *server.PcieDeviceCollectionPcieDeviceCollection {
	// Empty array, not null: the Redfish client deserializes Members into a Vec
	// and rejects null.
	members := []server.OdataV4IdRef{}
	if h.rm.Identity().PCIeDeviceSerial != "" {
		members = append(members, server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Chassis/%s/PCIeDevices/%s", chassisID, dpuPCIeDeviceID),
		})
	}

	return &server.PcieDeviceCollectionPcieDeviceCollection{
		OdataContext:      "/redfish/v1/$metadata#PCIeDeviceCollection.PCIeDeviceCollection",
		OdataId:           fmt.Sprintf("/redfish/v1/Chassis/%s/PCIeDevices", chassisID),
		OdataType:         "#PCIeDeviceCollection.PCIeDeviceCollection",
		Name:              "PCIe Device Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}
}

// GetPCIeDevice returns the attached DPU as a PCIe device. The serial number is
// the match key NICo correlates against the DPU's system serial; Status must be
// present or some Redfish clients drop the device entirely.
func (h *handler) GetPCIeDevice(chassisID, deviceID string) (*server.PcieDeviceV1130PcieDevice, error) {
	id := h.rm.Identity()
	if id.PCIeDeviceSerial == "" || deviceID != dpuPCIeDeviceID {
		return nil, fmt.Errorf("pcie device not found: %s", deviceID)
	}

	return &server.PcieDeviceV1130PcieDevice{
		OdataContext: "/redfish/v1/$metadata#PCIeDevice.PCIeDevice",
		OdataId:      fmt.Sprintf("/redfish/v1/Chassis/%s/PCIeDevices/%s", chassisID, deviceID),
		OdataType:    "#PCIeDevice.v1_13_0.PCIeDevice",
		Id:           deviceID,
		Name:         "BlueField DPU",
		Manufacturer: util.Ptr(id.PCIeDeviceManufacturer),
		PartNumber:   util.Ptr(id.PCIeDevicePartNumber),
		SerialNumber: util.Ptr(id.PCIeDeviceSerial),
		Status: server.ResourceStatus{
			State:  util.Ptr(server.RESOURCESTATE_ENABLED),
			Health: util.Ptr(server.RESOURCEHEALTH_OK),
		},
	}, nil
}

// The FirmwareInventory member ids NICo reads while driving a DPU. BMC_Firmware
// is read during discovery (dpu_bmc_version); together with the CEC
// (Bluefield_FW_ERoT), NIC (DPU_NIC) and UEFI (DPU_UEFI) members it is
// EXACT-matched by check_fw_component_version against NICo's hardware_models
// config, which parks the DPU forever on a mismatch. DPU_SYS_IMAGE carries the
// base MAC and DPU_BSP the BSP release. Every version is annotation-driven.
const (
	dpuBMCFirmwareID      = "BMC_Firmware"
	dpuCECFirmwareID      = "Bluefield_FW_ERoT"
	dpuNICFirmwareID      = "DPU_NIC"
	dpuUEFIFirmwareID     = "DPU_UEFI"
	dpuSysImageFirmwareID = "DPU_SYS_IMAGE"
	dpuBSPFirmwareID      = "DPU_BSP"
)

var dpuFirmwareIDs = []string{
	dpuBMCFirmwareID,
	dpuCECFirmwareID,
	dpuNICFirmwareID,
	dpuUEFIFirmwareID,
	dpuSysImageFirmwareID,
	dpuBSPFirmwareID,
}

func (h *handler) GetFirmwareInventoryCollection() *server.SoftwareInventoryCollectionSoftwareInventoryCollection {
	// Only a DPU advertises firmware; a non-DPU keeps an empty inventory. Members
	// must serialize as an empty array, not null: the Redfish client deserializes
	// it into a Vec and rejects null (fails the whole host exploration).
	members := []server.OdataV4IdRef{}
	if h.rm.Identity().DPU {
		for _, id := range dpuFirmwareIDs {
			members = append(members, server.OdataV4IdRef{
				OdataId: fmt.Sprintf("/redfish/v1/UpdateService/FirmwareInventory/%s", id),
			})
		}
	}

	return &server.SoftwareInventoryCollectionSoftwareInventoryCollection{
		OdataContext:      "/redfish/v1/$metadata#SoftwareInventoryCollection.SoftwareInventoryCollection",
		OdataId:           "/redfish/v1/UpdateService/FirmwareInventory",
		OdataType:         "#SoftwareInventoryCollection.SoftwareInventoryCollection",
		Name:              "Firmware Inventory Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}
}

func (h *handler) GetFirmwareInventory(inventoryID string) (*server.SoftwareInventoryV1101SoftwareInventory, error) {
	id := h.rm.Identity()
	if !id.DPU {
		return nil, fmt.Errorf("unknown firmware inventory id: %s", inventoryID)
	}

	var name, version string
	switch inventoryID {
	case dpuBMCFirmwareID:
		name, version = "BMC Firmware", id.BMCFirmware
	case dpuCECFirmwareID:
		name, version = "ERoT Firmware", id.CECFirmware
	case dpuNICFirmwareID:
		name, version = "NIC Firmware", id.NICFirmware
	case dpuUEFIFirmwareID:
		name, version = "UEFI Firmware", id.UEFIFirmware
	case dpuSysImageFirmwareID:
		name, version = "System Image", id.SysImageFirmware
	case dpuBSPFirmwareID:
		name, version = "BSP Firmware", id.BSPFirmware
	default:
		return nil, fmt.Errorf("unknown firmware inventory id: %s", inventoryID)
	}

	return &server.SoftwareInventoryV1101SoftwareInventory{
		OdataContext: "/redfish/v1/$metadata#SoftwareInventory.SoftwareInventory",
		OdataId:      fmt.Sprintf("/redfish/v1/UpdateService/FirmwareInventory/%s", inventoryID),
		OdataType:    "#SoftwareInventory.v1_10_1.SoftwareInventory",
		Id:           inventoryID,
		Name:         name,
		Version:      util.Ptr(version),
	}, nil
}

// bfbTaskID is the single task this shim ever reports. Firmware/BFB install is a
// no-op here: there is nothing to track, so SimpleUpdate and every task lookup
// resolve to the same already-completed task.
const bfbTaskID = "1"

func (h *handler) GetUpdateService() *server.UpdateServiceV1130UpdateService {
	return &server.UpdateServiceV1130UpdateService{
		OdataContext:   "/redfish/v1/$metadata#UpdateService.UpdateService",
		OdataId:        "/redfish/v1/UpdateService",
		OdataType:      "#UpdateService.v1_13_0.UpdateService",
		Id:             "UpdateService",
		Name:           "Update Service",
		ServiceEnabled: util.Ptr(true),
		FirmwareInventory: server.OdataV4IdRef{
			OdataId: "/redfish/v1/UpdateService/FirmwareInventory",
		},
		Actions: server.UpdateServiceV1130Actions{
			UpdateServiceSimpleUpdate: server.UpdateServiceV1130SimpleUpdate{
				Target: "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate",
				Title:  "SimpleUpdate",
			},
		},
	}
}

// SimpleUpdate shims a firmware/BFB install as an immediate success: it does no
// work and returns a completed task. The Redfish client reads the task id from
// this body and then polls the task, which also reports completed.
func (h *handler) SimpleUpdate() *server.TaskV173Task {
	return h.completedTask(bfbTaskID)
}

func (h *handler) GetTask(taskID string) *server.TaskV173Task {
	return h.completedTask(taskID)
}

func (h *handler) completedTask(taskID string) *server.TaskV173Task {
	return &server.TaskV173Task{
		OdataContext:    "/redfish/v1/$metadata#Task.Task",
		OdataId:         fmt.Sprintf("/redfish/v1/TaskService/Tasks/%s", taskID),
		OdataType:       "#Task.v1_7_3.Task",
		Id:              taskID,
		Name:            "Update Task",
		TaskState:       server.TASKV173TASKSTATE_COMPLETED,
		PercentComplete: util.Ptr(int64(100)),
	}
}

func (h *handler) GetTaskService() *server.TaskServiceV120TaskService {
	return &server.TaskServiceV120TaskService{
		OdataContext:   "/redfish/v1/$metadata#TaskService.TaskService",
		OdataId:        "/redfish/v1/TaskService",
		OdataType:      "#TaskService.v1_2_0.TaskService",
		Id:             "TaskService",
		Name:           "Task Service",
		ServiceEnabled: util.Ptr(true),
		Tasks: server.OdataV4IdRef{
			OdataId: "/redfish/v1/TaskService/Tasks",
		},
	}
}

func (h *handler) GetTaskCollection() *server.TaskCollectionTaskCollection {
	return &server.TaskCollectionTaskCollection{
		OdataContext: "/redfish/v1/$metadata#TaskCollection.TaskCollection",
		OdataId:      "/redfish/v1/TaskService/Tasks",
		OdataType:    "#TaskCollection.TaskCollection",
		Name:         "Task Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: fmt.Sprintf("/redfish/v1/TaskService/Tasks/%s", bfbTaskID),
			},
		},
		MembersodataCount: 1,
	}
}

// HandleSystemOEM returns the NVIDIA OEM resource NICo reads the DPU's NIC mode,
// base MAC and host rshim state from (GET /redfish/v1/Systems/Bluefield/Oem/Nvidia).
// The Redfish spec has no generated route for this vendor resource, so it is
// served via a custom route registered in the emulator. Mode is reported in the
// DpuMode/NicMode form libredfish parses; BaseMAC is the pf0 MAC with colons
// stripped.
func (h *handler) HandleSystemOEM(w http.ResponseWriter, r *http.Request) {
	id := h.rm.Identity()
	if !id.DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Mirror nico's reference mock (bmc-mock NvidiaComputerSystem) field-for-field,
	// including @odata.type. (The deserializer reads into a generic map and does not
	// require @odata.type, but matching the reference avoids any client that does.)
	body := map[string]interface{}{
		"@odata.id":   fmt.Sprintf("/redfish/v1/Systems/%s/Oem/Nvidia", id.SystemID),
		"@odata.type": "#NvidiaComputerSystem.v1_0_0.NvidiaComputerSystem",
		"Mode":        id.Mode,
		"BaseMAC":     strings.ReplaceAll(id.BaseMAC, ":", ""),
		"HostRshim":   id.HostRshim,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// dpuSecureBootID and dpuBiosID are the resource ids a DPU presents under its
// ComputerSystem. biosHostPrivilegeLevel and biosInternalCPUModel are the BIOS
// attribute values nico's is_bios_setup verifies (see GetBios).
const (
	dpuSecureBootID        = "SecureBoot"
	dpuBiosID              = "Bios"
	biosHostPrivilegeLevel = "Restricted"
	biosInternalCPUModel   = "Embedded"
)

// Host-BMC (HPE) presentation constants. hostManagerFirmwareVersion is the iLO
// version string nico reads from the Manager; hostBootOptionId/Reference identify
// the single HTTP/IPv4 boot option a host advertises for its boot NIC, whose
// reference is also Boot.BootOrder[0] (see GetComputerSystem).
const (
	hostManagerFirmwareVersion = "iLO 6 v1.58"
	hostBootOptionId           = "1"
	hostBootOptionReference    = "Boot0001"

	valueEnabled = "Enabled"
)

// GetSecureBoot reports the DPU's UEFI Secure Boot state. nico's machine-controller
// (CheckSecureBootStatus) reads both SecureBootEnable and SecureBootCurrentBoot;
// if either is absent it treats the response as the known BlueField "incomplete
// SecureBoot after UEFI client" race and force-restarts the DPU up to ten times
// before erroring. The current boot state follows the setting immediately instead
// of after the next boot, since the shim has no real boot. Only a DPU serves this.
func (h *handler) GetSecureBoot() (*server.SecureBootV111SecureBoot, error) {
	id := h.rm.Identity()
	if !id.DPU {
		return nil, fmt.Errorf("secure boot not supported")
	}

	currentBoot := server.SECUREBOOTV111SECUREBOOTCURRENTBOOTTYPE_DISABLED
	if id.SecureBoot {
		currentBoot = server.SECUREBOOTV111SECUREBOOTCURRENTBOOTTYPE_ENABLED
	}

	return &server.SecureBootV111SecureBoot{
		OdataContext:          "/redfish/v1/$metadata#SecureBoot.SecureBoot",
		OdataId:               fmt.Sprintf("/redfish/v1/Systems/%s/SecureBoot", id.SystemID),
		OdataType:             "#SecureBoot.v1_1_1.SecureBoot",
		Id:                    dpuSecureBootID,
		Name:                  "UEFI Secure Boot",
		SecureBootEnable:      util.Ptr(id.SecureBoot),
		SecureBootCurrentBoot: currentBoot,
	}, nil
}

// GetBios reports the DPU BIOS attributes nico's is_bios_setup verifies in
// PollingBiosSetup: it reads Systems/<id>/Bios and requires HostPrivilegeLevel
// "Restricted" and InternalCPUModel "Embedded" before advancing the DPU to
// WaitingForNetworkConfig. The shim models no real BIOS, so it reports the
// already-applied values; the writes that would set them (Bios/Settings) are
// accepted as no-ops. NicMode follows the Mode.Set state for clients that read
// the mode from the BIOS.
func (h *handler) GetBios() (*server.BiosV122Bios, error) {
	id := h.rm.Identity()

	if id.DPU {
		return &server.BiosV122Bios{
			OdataContext: "/redfish/v1/$metadata#Bios.Bios",
			OdataId:      fmt.Sprintf("/redfish/v1/Systems/%s/Bios", id.SystemID),
			OdataType:    "#Bios.v1_2_2.Bios",
			Id:           dpuBiosID,
			Name:         "BIOS Configuration",
			Attributes: map[string]interface{}{
				"NicMode":             id.Mode,
				"HostPrivilegeLevel":  biosHostPrivilegeLevel,
				"InternalCPUModel":    biosInternalCPUModel,
				"CurrentUefiPassword": "",
			},
		}, nil
	}

	// Host BIOS attributes nico's HPE client verifies during HostInit, all read
	// back exactly as served (the writes that would set them, PATCH
	// Systems/1/Bios/settings, are accepted as no-ops):
	//   - UsbBoot "Enabled": with Manager Oem.Hpe.VirtualNICEnabled true, makes
	//     lockdown_status read as "fully disabled".
	//   - ProcVirtualization/Dhcpv4/HttpSupport: the PollingBiosSetup exact-match
	//     gate (get_virt_enabled needs one of IntelProcVtd/ProcAmdIoVt/
	//     ProcVirtualization == "Enabled"; Dhcpv4 == "Enabled"; HttpSupport ==
	//     "Auto").
	// No Actions block is served, so nico's UEFI-password step no-ops cleanly.
	return &server.BiosV122Bios{
		OdataContext: "/redfish/v1/$metadata#Bios.Bios",
		OdataId:      fmt.Sprintf("/redfish/v1/Systems/%s/Bios", id.SystemID),
		OdataType:    "#Bios.v1_2_2.Bios",
		Id:           dpuBiosID,
		Name:         "BIOS Configuration",
		Attributes: map[string]interface{}{
			"UsbBoot":            valueEnabled,
			"ProcVirtualization": valueEnabled,
			"Dhcpv4":             valueEnabled,
			"HttpSupport":        "Auto",
		},
	}, nil
}

// HandleDPUConfigNoop accepts the DPU BIOS/BMC mutations nico issues while
// driving a DPU to WaitingForNetworkConfig that have nothing to persist: the BIOS
// attribute writes (Systems/<id>/Bios/Settings), whose values are served as
// already applied, the BMC rshim enable (Managers/<id>/Oem/Nvidia) and the
// AccountService lockout writes. They are acknowledged with 204. None of these
// paths have a usable route in the generated Redfish router, so they are
// registered directly in the emulator. A non-DPU BMC never receives them.
func (h *handler) HandleDPUConfigNoop(w http.ResponseWriter, r *http.Request) {
	if !h.rm.Identity().DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetManagerNetworkProtocol reports IPMI-over-LAN as already enabled so nico's
// HostInit EnableIpmiOverLan step (which reads only IPMI.ProtocolEnabled) skips
// its enable PATCH. This is the endpoint the host BMC previously 501'd on,
// stalling the host at EnableIpmiOverLan.
func (h *handler) GetManagerNetworkProtocol(managerID string) *server.ManagerNetworkProtocolV1100ManagerNetworkProtocol {
	return &server.ManagerNetworkProtocolV1100ManagerNetworkProtocol{
		OdataContext: "/redfish/v1/$metadata#ManagerNetworkProtocol.ManagerNetworkProtocol",
		OdataId:      fmt.Sprintf("/redfish/v1/Managers/%s/NetworkProtocol", managerID),
		OdataType:    "#ManagerNetworkProtocol.v1_10_0.ManagerNetworkProtocol",
		Id:           "NetworkProtocol",
		Name:         "Manager Network Protocol",
		IPMI: server.ManagerNetworkProtocolV1100Protocol{
			ProtocolEnabled: util.Ptr(true),
		},
	}
}

// GetBootOptionCollection lists the host's single HTTP/IPv4 boot option. nico's
// SetBootOrder scans the members for a DisplayName containing HTTP + IPV4 + the
// boot NIC MAC. Host-only, and only when the boot MAC is known; a DPU never reads
// boot options. Members must serialize as an empty array, not null.
func (h *handler) GetBootOptionCollection() (*server.BootOptionCollectionBootOptionCollection, error) {
	id := h.rm.Identity()
	if id.DPU {
		return nil, fmt.Errorf("boot options not supported")
	}

	members := []server.OdataV4IdRef{}
	if id.HostBootMAC != "" {
		members = append(members, server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/BootOptions/%s", id.SystemID, hostBootOptionId),
		})
	}

	return &server.BootOptionCollectionBootOptionCollection{
		OdataContext:      "/redfish/v1/$metadata#BootOptionCollection.BootOptionCollection",
		OdataId:           fmt.Sprintf("/redfish/v1/Systems/%s/BootOptions", id.SystemID),
		OdataType:         "#BootOptionCollection.BootOptionCollection",
		Name:              "Boot Option Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}, nil
}

// GetBootOption returns the host's HTTP/IPv4 boot option for the boot NIC. Its
// DisplayName carries HTTP, IPV4 and the uppercased, colon-separated boot MAC so
// nico's SetBootOrder matches it, and its BootOptionReference equals
// Boot.BootOrder[0] so CheckBootOrder passes. Host-only.
func (h *handler) GetBootOption(optionID string) (*server.BootOptionV105BootOption, error) {
	id := h.rm.Identity()
	if id.DPU || id.HostBootMAC == "" || optionID != hostBootOptionId {
		return nil, fmt.Errorf("boot option not found: %s", optionID)
	}

	displayName := fmt.Sprintf("Boot NIC %s (HTTP(S) IPv4)", strings.ToUpper(id.HostBootMAC))
	return &server.BootOptionV105BootOption{
		OdataContext:        "/redfish/v1/$metadata#BootOption.BootOption",
		OdataId:             fmt.Sprintf("/redfish/v1/Systems/%s/BootOptions/%s", id.SystemID, optionID),
		OdataType:           "#BootOption.v1_0_5.BootOption",
		Id:                  optionID,
		Name:                "Boot Option",
		BootOptionReference: util.Ptr(hostBootOptionReference),
		BootOptionEnabled:   util.Ptr(true),
		DisplayName:         util.Ptr(displayName),
	}, nil
}

// HandleHostConfigNoop accepts the HPE BIOS/boot mutations nico issues during
// HostInit as no-ops (204): the machine_setup BIOS writes (PATCH
// Systems/<id>/Bios/settings and .../settings/ — serial console, TPM clear,
// virtualization, UEFI HTTP NIC boot) and the boot-order write (PATCH
// Systems/<id>/Bios/oem/hpe/boot/settings/). The shim models no real BIOS and
// serves the applied values statically, so there is nothing to persist. HPE uses
// lowercase, trailing-slash paths with no generated route, so these are
// registered directly in the emulator. A DPU BMC never receives them.
func (h *handler) HandleHostConfigNoop(w http.ResponseWriter, r *http.Request) {
	if h.rm.Identity().DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleHostBiosOemBoot serves the HPE proprietary boot-order resource nico reads
// while assembling the machine_setup reorder (GET Systems/<id>/Bios/oem/hpe/boot/).
// The three arrays must serialize as [] not null or the client rejects the body;
// their content is irrelevant to the gates (the reorder PATCH is a no-op), so
// they are served empty. No generated route exists for this path, so it is
// registered directly in the emulator. Host-only.
func (h *handler) HandleHostBiosOemBoot(w http.ResponseWriter, r *http.Request) {
	id := h.rm.Identity()
	if id.DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	body := map[string]interface{}{
		"@odata.id":                 fmt.Sprintf("/redfish/v1/Systems/%s/Bios/oem/hpe/boot/", id.SystemID),
		"@odata.type":               "#HpeServerBootSettings.v2_0_0.HpeServerBootSettings",
		"Id":                        "boot",
		"Name":                      "Boot Order Current Settings",
		"PersistentBootConfigOrder": []string{},
		"BootSources":               []interface{}{},
		"DefaultBootOrder":          []string{},
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) GetManagerEthernetInterfaceCollection(managerID string) *server.EthernetInterfaceCollectionEthernetInterfaceCollection {
	return &server.EthernetInterfaceCollectionEthernetInterfaceCollection{
		OdataContext:      "/redfish/v1/$metadata#EthernetInterfaceCollection.EthernetInterfaceCollection",
		OdataId:           fmt.Sprintf("/redfish/v1/Managers/%s/EthernetInterfaces", managerID),
		OdataType:         "#EthernetInterfaceCollection.EthernetInterfaceCollection",
		Name:              "Ethernet Interface Collection",
		Members:           []server.OdataV4IdRef{},
		MembersodataCount: 0,
	}
}
