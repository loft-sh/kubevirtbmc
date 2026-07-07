package resourcemanager

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiclient "kubevirt.io/client-go/containerizeddataimporter"
	kvclient "kubevirt.io/client-go/kubevirt"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

const (
	defaultComputerSystemId = "1"
	defaultManagerId        = "BMC"
	defaultManagerName      = "Manager"
	defaultVirtualMediaId   = "CD1"
	defaultVirtualMediaName = "Virtual Media"

	// bluefieldComputerSystemId is the ComputerSystem id NVIDIA's Redfish clients
	// match on (case-sensitive) to recognize a BlueField DPU. A DPU-flagged VM
	// presents this instead of defaultComputerSystemId.
	bluefieldComputerSystemId = "Bluefield"
	// dpuCardChassisId is the chassis id ("Card1") NVIDIA's clients read the DPU
	// model from.
	dpuCardChassisId = "Card1"
)

// Annotation keys, all on the KubeVirt VM, that drive the mocked Redfish identity.
// Each is optional; absent keys fall back to the non-DPU/plain-host behavior.
const (
	annotationPrefix = "bmc.kubevirtbmc.io/"

	annotationDPU                = annotationPrefix + "dpu"
	annotationSystemSerial       = annotationPrefix + "system-serial"
	annotationDPUModel           = annotationPrefix + "dpu-model"
	annotationNicMode            = annotationPrefix + "nic-mode"
	annotationBaseMAC            = annotationPrefix + "base-mac"
	annotationPCIeDeviceSerial   = annotationPrefix + "pcie-device-serial"
	annotationBMCFirmwareVersion = annotationPrefix + "firmware-version"
	annotationCECFirmwareVersion = annotationPrefix + "firmware-version-cec"
	annotationNICFirmwareVersion = annotationPrefix + "firmware-version-nic"
)

var (
	powerStateMap = map[bool]server.ResourcePowerState{
		true:  server.RESOURCEPOWERSTATE_ON,
		false: server.RESOURCEPOWERSTATE_OFF,
	}
	bootSourceMap = map[BootDevice]server.ComputerSystemBootSource{
		BootDevicePxe: server.COMPUTERSYSTEMBOOTSOURCE_PXE,
		BootDeviceHdd: server.COMPUTERSYSTEMBOOTSOURCE_HDD,
		BootDeviceCd:  server.COMPUTERSYSTEMBOOTSOURCE_CD,
	}
)

type VirtualMachineResourceManager struct {
	ctx        context.Context
	virtClient kvclient.Interface
	cdiClient  cdiclient.Interface

	namespace string
	name      string

	computerSystem ComputerSystemInterface
	manager        ManagerInterface
	virtualMedia   VirtualMediaInterface

	// Mocked identity derived from VM annotations at Initialize. dpu gates the
	// BlueField presentation; the remaining fields are only meaningful when set.
	dpu                bool
	dpuModel           string
	nicMode            string
	baseMAC            string
	pcieDeviceSerial   string
	bmcFirmwareVersion string
	cecFirmwareVersion string
	nicFirmwareVersion string
}

func NewVirtualMachineResourceManager(
	ctx context.Context,
	virtClient kvclient.Interface,
	cdiClient cdiclient.Interface,
) *VirtualMachineResourceManager {
	return &VirtualMachineResourceManager{
		ctx:        ctx,
		virtClient: virtClient,
		cdiClient:  cdiClient,
	}
}

func (m *VirtualMachineResourceManager) Initialize(namespace, name string) error {
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(namespace).Get(m.ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	m.namespace = vm.Namespace
	m.name = vm.Name

	// Mocked Redfish identity from VM annotations (lab use). Absent keys fall back
	// to the plain-VM behavior.
	annotations := vm.Annotations
	m.dpu = annotations[annotationDPU] == "true"
	m.dpuModel = annotations[annotationDPUModel]
	m.nicMode = annotations[annotationNicMode]
	m.baseMAC = annotations[annotationBaseMAC]
	m.pcieDeviceSerial = annotations[annotationPCIeDeviceSerial]
	m.bmcFirmwareVersion = annotations[annotationBMCFirmwareVersion]
	m.cecFirmwareVersion = annotations[annotationCECFirmwareVersion]
	m.nicFirmwareVersion = annotations[annotationNICFirmwareVersion]

	// Detect boot mode from VM firmware configuration
	bootMode := m.detectBootMode(vm)

	// The matching key. Prefer the explicit annotation; otherwise fall back to the
	// VM UID with dashes stripped to fit the 32-char serial limit some BMC
	// databases impose.
	serial := annotations[annotationSystemSerial]
	if serial == "" {
		serial = strings.ReplaceAll(string(vm.UID), "-", "")
	}

	// A DPU presents the "Bluefield" ComputerSystem id NVIDIA's clients key on.
	computerSystemId := defaultComputerSystemId
	if m.dpu {
		computerSystemId = bluefieldComputerSystemId
	}

	// Initialize computer system
	m.computerSystem = NewComputerSystem(
		computerSystemId,
		strings.Join([]string{vm.Namespace, vm.Name}, "/"),
		serial,
		powerStateMap[vm.Status.Ready],
		bootMode,
	)

	// A DPU advertises the NVIDIA OEM resource on its ComputerSystem so clients
	// follow the reference to read NIC mode and base MAC. The resource itself is
	// served by a custom route in the Redfish emulator.
	if m.dpu {
		if adapter, ok := m.computerSystem.(*ComputerSystemAdapter); ok {
			adapter.ComputerSystem().Oem = map[string]interface{}{
				"Nvidia": map[string]interface{}{
					"@odata.id": fmt.Sprintf("/redfish/v1/Systems/%s/Oem/Nvidia", computerSystemId),
				},
			}
			// nico's did_dpu_finish_booting parks the DPU ("waiting for DPU to
			// come back up") unless BootProgress.LastState reports the OS running.
			// The shim has no real boot, so it reports the DPU as booted.
			adapter.ComputerSystem().BootProgress = server.ComputerSystemV1220BootProgress{
				LastState: server.COMPUTERSYSTEMV1220BOOTPROGRESSTYPES_OS_RUNNING,
			}
		}
	}

	// Initialize manager
	m.manager = NewManager(defaultManagerId, defaultManagerName)

	// Initialize virtual media
	m.virtualMedia = NewVirtualMedia(defaultVirtualMediaId, defaultVirtualMediaName)

	// Build relationships
	var (
		oDataComputerSystem OdataInterface = m.computerSystem
		oDataManager        OdataInterface = m.manager
	)
	if err := oDataComputerSystem.ManagedBy(oDataManager); err != nil {
		return err
	}
	if err := oDataManager.Manage(oDataComputerSystem); err != nil {
		return err
	}

	return nil
}

func (m *VirtualMachineResourceManager) GetComputerSystem() (ComputerSystemInterface, error) {
	if m.computerSystem == nil {
		return nil, fmt.Errorf("computer system not initialized")
	}

	// Update the power state just-in-time until we actually implement a control loop for it
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	switch vm.Status.Ready {
	case true:
		m.computerSystem.SetPowerState(server.RESOURCEPOWERSTATE_ON)
	case false:
		m.computerSystem.SetPowerState(server.RESOURCEPOWERSTATE_OFF)
	}

	// Update boot mode based on current VM firmware configuration
	bootMode := m.detectBootMode(vm)
	m.computerSystem.SetBootMode(bootMode)

	return m.computerSystem, nil
}

func (m *VirtualMachineResourceManager) GetManager() (ManagerInterface, error) {
	return m.manager, nil
}

func (m *VirtualMachineResourceManager) GetVirtualMedia() (VirtualMediaInterface, error) {
	return m.virtualMedia, nil
}

func (m *VirtualMachineResourceManager) EjectMedia() error {
	if m.virtualMedia == nil {
		return fmt.Errorf("virtual media not initialized")
	}

	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}

	cdromDisk, err := util.GetCdromDisk(vm.Spec.Template.Spec.Domain.Devices.Disks)
	if err != nil {
		return err
	}

	var dvName string
	vm.Spec.Template.Spec.Volumes = slices.DeleteFunc(vm.Spec.Template.Spec.Volumes, func(v kubevirtv1.Volume) bool {
		if v.Name == cdromDisk.Name {
			dvName = v.DataVolume.Name
			return true
		}
		return false
	})

	if dvName == "" {
		return fmt.Errorf("no media inserted")
	}

	if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Update(m.ctx, vm, metav1.UpdateOptions{}); err != nil {
		return err
	}

	if err := m.cdiClient.CdiV1beta1().DataVolumes(m.namespace).Delete(m.ctx, dvName, metav1.DeleteOptions{}); err != nil {
		return err
	}

	m.virtualMedia.SetVirtualMedia("", false)

	return nil
}

func (m *VirtualMachineResourceManager) InsertMedia(imageURL string) error {
	if m.virtualMedia == nil {
		return fmt.Errorf("virtual media not initialized")
	}

	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}

	cdromDisk, err := util.GetCdromDisk(vm.Spec.Template.Spec.Domain.Devices.Disks)
	if err != nil {
		return err
	}

	imageSize, err := util.GetRemoteFileSize(imageURL)
	if err != nil {
		return err
	}

	// Create DataVolume
	dv := util.ConstructDataVolume(m.namespace, m.name, imageURL, imageSize)
	_, err = m.cdiClient.CdiV1beta1().DataVolumes(m.namespace).Create(m.ctx, dv, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	// Attach DataVolume to VirtualMachine
	volume := kubevirtv1.Volume{
		Name: cdromDisk.Name,
		VolumeSource: kubevirtv1.VolumeSource{
			DataVolume: &kubevirtv1.DataVolumeSource{
				Name:         dv.Name,
				Hotpluggable: true,
			},
		},
	}
	vm.Spec.Template.Spec.Volumes = append(vm.Spec.Template.Spec.Volumes, volume)

	if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Update(m.ctx, vm, metav1.UpdateOptions{}); err != nil {
		return err
	}

	m.virtualMedia.SetVirtualMedia(imageURL, true)

	return nil
}

func (m *VirtualMachineResourceManager) GetPowerStatus() (bool, error) {
	// TODO: Implement a control loop to keep the power state in sync, then we will be able to
	// return the power state from the intermediate object, i.e. ComputerSystem.
	//
	// ps := m.computerSystem.GetPowerState()
	// switch ps {
	// case server.RESOURCEPOWERSTATE_ON, server.RESOURCEPOWERSTATE_POWERING_ON:
	// 	return true, nil
	// case server.RESOURCEPOWERSTATE_OFF, server.RESOURCEPOWERSTATE_POWERING_OFF:
	// 	return false, nil
	// default:
	// 	return false, nil
	// }
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	return vm.Status.Ready, nil
}

func (m *VirtualMachineResourceManager) PowerOn() error {
	_, err := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get VMI %s/%s: %w", m.namespace, m.name, err)
	}
	return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Start(m.ctx, m.name, &kubevirtv1.StartOptions{})
}

func (m *VirtualMachineResourceManager) PowerOff() error {
	_, err := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get VMI %s/%s: %w", m.namespace, m.name, err)
	}
	return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Stop(m.ctx, m.name, &kubevirtv1.StopOptions{})
}

func (m *VirtualMachineResourceManager) PowerCycle() error {
	isUp, err := m.GetPowerStatus()
	if err != nil {
		return err
	}
	if !isUp {
		return m.PowerOn()
	}
	return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Restart(m.ctx, m.name, &kubevirtv1.RestartOptions{})
}

func (m *VirtualMachineResourceManager) detectBootMode(vm *kubevirtv1.VirtualMachine) server.ComputerSystemV1220BootSourceOverrideMode {
	// Check if VM has EFI firmware configured
	if vm.Spec.Template != nil &&
		vm.Spec.Template.Spec.Domain.Firmware != nil &&
		vm.Spec.Template.Spec.Domain.Firmware.Bootloader != nil &&
		vm.Spec.Template.Spec.Domain.Firmware.Bootloader.EFI != nil {
		return server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_UEFI
	}
	return server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEMODE_LEGACY
}

func (m *VirtualMachineResourceManager) IsDPU() bool {
	return m.dpu
}

func (m *VirtualMachineResourceManager) DPUCard() string {
	return m.dpuModel
}

func (m *VirtualMachineResourceManager) SystemOEM() (nicMode, baseMAC string) {
	return m.nicMode, m.baseMAC
}

func (m *VirtualMachineResourceManager) AttachedDPUSerial() (string, bool) {
	return m.pcieDeviceSerial, m.pcieDeviceSerial != ""
}

func (m *VirtualMachineResourceManager) BMCFirmwareVersion() (string, bool) {
	return m.bmcFirmwareVersion, m.bmcFirmwareVersion != ""
}

func (m *VirtualMachineResourceManager) CECFirmwareVersion() (string, bool) {
	return m.cecFirmwareVersion, m.cecFirmwareVersion != ""
}

func (m *VirtualMachineResourceManager) NICFirmwareVersion() (string, bool) {
	return m.nicFirmwareVersion, m.nicFirmwareVersion != ""
}

func (m *VirtualMachineResourceManager) GetEthernetInterfaces() ([]EthernetInterfaceInterface, error) {
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get ethernet interfaces: %w", err)
	}

	if vm.Spec.Template == nil {
		return []EthernetInterfaceInterface{}, nil
	}

	interfaces := vm.Spec.Template.Spec.Domain.Devices.Interfaces
	if len(interfaces) == 0 {
		return []EthernetInterfaceInterface{}, nil
	}

	running := vm.Status.Ready
	var linkStatus server.EthernetInterfaceV1120LinkStatus
	if running {
		linkStatus = server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_UP
	} else {
		linkStatus = server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_DOWN
	}

	result := make([]EthernetInterfaceInterface, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.MacAddress == "" {
			logrus.WithField("interface", iface.Name).Warn("Skipping interface without MAC address")
			continue
		}

		ethernetIface := NewEthernetInterface(
			iface.Name,
			iface.Name,
			iface.MacAddress,
			running,
			linkStatus,
		)
		result = append(result, ethernetIface)
	}

	return result, nil
}

func (m *VirtualMachineResourceManager) SetBootDevice(bootDevice BootDevice) error {
	logrus.Info("SetBootDevice")
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}

	for i, intf := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		logrus.Infof("interface: %+v", intf)
		vm.Spec.Template.Spec.Domain.Devices.Interfaces[i].BootOrder = nil
	}
	for i, dev := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		logrus.Infof("disk: %+v", dev)
		vm.Spec.Template.Spec.Domain.Devices.Disks[i].BootOrder = nil
	}

	var firstOrder uint = 1
	switch bootDevice {
	case BootDevicePxe:
		if vm.Spec.Template.Spec.Domain.Devices.Interfaces == nil {
			return fmt.Errorf("no interfaces found")
		}
		vm.Spec.Template.Spec.Domain.Devices.Interfaces[0].BootOrder = &firstOrder
		logrus.Infof("To be updated vm: %+v", vm.Spec.Template.Spec.Domain.Devices.Interfaces[0])
	case BootDeviceHdd:
		if vm.Spec.Template.Spec.Domain.Devices.Disks == nil {
			return fmt.Errorf("no disks found")
		}
		vm.Spec.Template.Spec.Domain.Devices.Disks[0].BootOrder = &firstOrder
	case BootDeviceCd:
		cdromDisk, err := util.GetCdromDisk(vm.Spec.Template.Spec.Domain.Devices.Disks)
		if err != nil {
			return fmt.Errorf("no cdrom found: %w", err)
		}
		for i, disk := range vm.Spec.Template.Spec.Domain.Devices.Disks {
			if disk.Name == cdromDisk.Name {
				vm.Spec.Template.Spec.Domain.Devices.Disks[i].BootOrder = &firstOrder
				break
			}
		}
	}

	if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Update(m.ctx, vm, metav1.UpdateOptions{}); err != nil {
		logrus.Errorf("update vm error: %v", err)
		return err
	}

	if m.computerSystem == nil {
		logrus.Warn("computer system not initialized")
		return nil
	}
	m.computerSystem.SetBootOverride(bootSourceMap[bootDevice])

	return nil
}
