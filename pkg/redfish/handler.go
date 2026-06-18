package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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
		// Report a known server vendor rather than "KubeVirt". NVIDIA's
		// libredfish client (used by NICo) treats an unrecognized vendor as
		// "Unknown" and then builds the exploration client without running its
		// init path, so it never discovers the system/manager ids and requests
		// Managers/ (empty id), failing with "missing field Id". A recognized
		// vendor takes the full-init path that discovers those ids. HPE is the
		// safest impersonation: its libredfish client delegates the exploration
		// reads to the standard Redfish paths this emulator implements, unlike
		// Dell/Lenovo which read vendor-specific BIOS attributes and error out.
		Vendor: util.Ptr("HPE"),
		UUID:   util.Ptr("00000000-0000-0000-0000-000000000000"),
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
	return &server.ManagerCollectionManagerCollection{
		OdataContext: "/redfish/v1/$metadata#ManagerCollection.ManagerCollection",
		OdataId:      "/redfish/v1/Managers",
		OdataType:    "#ManagerCollection.ManagerCollection",
		Description:  "Manager Collection",
		Name:         "Manager Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: "/redfish/v1/Managers/BMC",
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
	// The member id must match the ComputerSystem's own id so a DPU is listed as
	// "Bluefield" rather than the default "1".
	systemID := defaultComputerSystemId
	if h.rm.IsDPU() {
		systemID = bluefieldComputerSystemId
	}

	return &server.ComputerSystemCollectionComputerSystemCollection{
		OdataContext: "/redfish/v1/$metadata#ComputerSystemCollection.ComputerSystemCollection",
		OdataId:      "/redfish/v1/Systems",
		OdataType:    "#ComputerSystemCollection.ComputerSystemCollection",
		Description:  "Computer System Collection",
		Name:         "Computer System Collection",
		Members: []server.OdataV4IdRef{
			{
				OdataId: fmt.Sprintf("/redfish/v1/Systems/%s", systemID),
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

// dpuPCIeDeviceID is the single PCIe device a host BMC reports: the attached
// DPU. Its serial number is what NICo matches against the DPU's system serial.
const dpuPCIeDeviceID = "DPU"

func (h *handler) GetChassisCollection() *server.ChassisCollectionChassisCollection {
	members := []server.OdataV4IdRef{
		{
			OdataId: "/redfish/v1/Chassis/1",
		},
	}

	// A DPU additionally exposes the "Card1" chassis NVIDIA's clients read the
	// BlueField model from.
	if h.rm.IsDPU() {
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
// chassis carries the BlueField model NVIDIA's clients classify on. References
// left unset are dropped by sanitize so clients see them as absent rather than an
// empty reference missing its required @odata.id.
func (h *handler) GetChassis(chassisID string) *server.ChassisV1250Chassis {
	if h.rm.IsDPU() && chassisID == dpuCardChassisId {
		model := h.rm.DPUCard()
		if model == "" {
			model = defaultDPUCardModel
		}
		return &server.ChassisV1250Chassis{
			OdataContext: "/redfish/v1/$metadata#Chassis.Chassis",
			OdataId:      fmt.Sprintf("/redfish/v1/Chassis/%s", chassisID),
			OdataType:    "#Chassis.v1_25_0.Chassis",
			Id:           chassisID,
			Name:         "Network Adapter Card",
			ChassisType:  server.CHASSISV1250CHASSISTYPE_CARD,
			Manufacturer: util.Ptr("NVIDIA"),
			Model:        util.Ptr(model),
		}
	}

	chassis := server.ChassisV1250Chassis{
		OdataContext: "/redfish/v1/$metadata#Chassis.Chassis",
		OdataId:      fmt.Sprintf("/redfish/v1/Chassis/%s", chassisID),
		OdataType:    "#Chassis.v1_25_0.Chassis",
		Id:           chassisID,
		Name:         "Computer System Chassis",
		ChassisType:  server.CHASSISV1250CHASSISTYPE_RACK_MOUNT,
		Manufacturer: util.Ptr("KubeVirt"),
	}

	// A host with an attached DPU points at its PCIe inventory so the Redfish
	// client follows the link and reads the DPU device beneath it.
	if _, ok := h.rm.AttachedDPUSerial(); ok {
		chassis.PCIeDevices = server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Chassis/%s/PCIeDevices", chassisID),
		}
	}

	return &chassis
}

func (h *handler) GetPCIeDeviceCollection(chassisID string) *server.PcieDeviceCollectionPcieDeviceCollection {
	var members []server.OdataV4IdRef
	if _, ok := h.rm.AttachedDPUSerial(); ok {
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
	serial, ok := h.rm.AttachedDPUSerial()
	if !ok || deviceID != dpuPCIeDeviceID {
		return nil, fmt.Errorf("pcie device not found: %s", deviceID)
	}

	return &server.PcieDeviceV1130PcieDevice{
		OdataContext: "/redfish/v1/$metadata#PCIeDevice.PCIeDevice",
		OdataId:      fmt.Sprintf("/redfish/v1/Chassis/%s/PCIeDevices/%s", chassisID, deviceID),
		OdataType:    "#PCIeDevice.v1_13_0.PCIeDevice",
		Id:           deviceID,
		Name:         "BlueField DPU",
		Manufacturer: util.Ptr("NVIDIA"),
		PartNumber:   util.Ptr("BlueField"),
		SerialNumber: util.Ptr(serial),
		Status: server.ResourceStatus{
			State:  util.Ptr(server.RESOURCESTATE_ENABLED),
			Health: util.Ptr(server.RESOURCEHEALTH_OK),
		},
	}, nil
}

// dpuBMCFirmwareID is the FirmwareInventory member nico reads the DPU BMC version
// from: dpu_bmc_version() matches the inventory id by the substring "BMC_Firmware".
const dpuBMCFirmwareID = "BMC_Firmware"

// defaultBMCFirmwareVersion is reported when a DPU has no firmware-version annotation.
// It is >= 24.10 so supports_bfb_install() passes and nico exercises the BFB path
// (which this shim no-ops).
const defaultBMCFirmwareVersion = "24.10.1"

func (h *handler) GetFirmwareInventoryCollection() *server.SoftwareInventoryCollectionSoftwareInventoryCollection {
	// Only a DPU advertises a BMC firmware version; nico reads it to gate BFB
	// install. Non-DPU VMs keep an empty inventory.
	var members []server.OdataV4IdRef
	if h.rm.IsDPU() {
		members = append(members, server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/UpdateService/FirmwareInventory/%s", dpuBMCFirmwareID),
		})
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
	// nico reads the BMC firmware version here to decide whether BFB install is
	// supported (requires >= 24.10). Only a DPU exposes it; the annotation drives
	// the value, defaulting to a >= 24.10 version so the BFB path is exercised.
	if !h.rm.IsDPU() || inventoryID != dpuBMCFirmwareID {
		return nil, fmt.Errorf("unknown firmware inventory id: %s", inventoryID)
	}

	version, ok := h.rm.BMCFirmwareVersion()
	if !ok {
		version = defaultBMCFirmwareVersion
	}

	return &server.SoftwareInventoryV1101SoftwareInventory{
		OdataContext: "/redfish/v1/$metadata#SoftwareInventory.SoftwareInventory",
		OdataId:      fmt.Sprintf("/redfish/v1/UpdateService/FirmwareInventory/%s", dpuBMCFirmwareID),
		OdataType:    "#SoftwareInventory.v1_10_1.SoftwareInventory",
		Id:           dpuBMCFirmwareID,
		Name:         "BMC Firmware",
		Version:      util.Ptr(version),
	}, nil
}

// bfbTaskID is the single task this shim ever reports. Firmware/BFB install is a
// no-op here: there is nothing to track, so SimpleUpdate and every task lookup
// resolve to the same already-completed task.
const bfbTaskID = "1"

// bluefieldComputerSystemId is the ComputerSystem id a DPU presents (NVIDIA's
// clients match it case-sensitively); dpuCardChassisId is the chassis id they
// read the BlueField model from. Both mirror the resourcemanager constants.
const (
	defaultComputerSystemId   = "1"
	bluefieldComputerSystemId = "Bluefield"
	dpuCardChassisId          = "Card1"
)

// defaultDPUCardModel is the Card1 model reported when a DPU has no dpu-model
// annotation. It mirrors bmc-mock; it classifies as DpuModel::Unknown (no space
// before "3"), which is benign — identify_dpu() gates only on the system id.
const defaultDPUCardModel = "BlueField-3 DPU"

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

// GetSystemOEM returns the NVIDIA OEM resource NICo reads the DPU's NIC mode and
// base MAC from (GET /redfish/v1/Systems/Bluefield/Oem/Nvidia). The Redfish spec
// has no generated route for this vendor resource, so it is served via a custom
// route registered in the emulator. Mode is reported in the DpuMode/NicMode form
// libredfish parses; BaseMAC is the pf0 MAC with colons stripped.
func (h *handler) HandleSystemOEM(w http.ResponseWriter, r *http.Request) {
	if !h.rm.IsDPU() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	nicMode, baseMAC := h.rm.SystemOEM()
	mode := "DpuMode"
	if strings.EqualFold(nicMode, "nic") {
		mode = "NicMode"
	}

	body := map[string]interface{}{
		"@odata.id": fmt.Sprintf("/redfish/v1/Systems/%s/Oem/Nvidia", bluefieldComputerSystemId),
		"Mode":      mode,
		"BaseMAC":   strings.ReplaceAll(baseMAC, ":", ""),
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
