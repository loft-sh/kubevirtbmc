package resourcemanager

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
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

	// bluefieldComputerSystemId and bluefieldManagerId are the ids NVIDIA's Redfish
	// clients match on (case-sensitive): a DPU-flagged VM presents them instead of
	// the defaults. libredfish also hardcodes the manager id in its rshim PATCH.
	bluefieldComputerSystemId = "Bluefield"
	bluefieldManagerId        = "Bluefield_BMC"

	// oobEthernetInterfaceID is the id a DPU serves its OOB NIC under; NICo
	// recognizes the OOB interface by "oob" in the id.
	oobEthernetInterfaceID = "oob_net0"

	// A one-time boot override is reverted once the restarted VMI exists. The
	// timeout only bounds a VM that never comes back.
	defaultBootRestoreInterval = time.Second
	defaultBootRestoreTimeout  = 10 * time.Minute
)

// hostBootInterfaceID and hostBootInterfaceUefiDevicePath describe the synthetic
// boot NIC a host BMC advertises. nico sets a host's primary (boot) interface by
// matching the paired DPU's base MAC against the host's EthernetInterfaces and
// requires the matching interface to carry a non-null, parseable UEFI/PCI device
// path (site_explorer fetch_host_primary_interface_mac); otherwise the boot
// interface never resolves and HostInit's SetBootOrder errors. The path only has
// to be a valid PciRoot(...)/Pci(...) string (its numeric form is used to pick
// the lowest-PCI interface); its exact value is otherwise irrelevant.
const (
	hostBootInterfaceID             = "boot"
	hostBootInterfaceUefiDevicePath = "PciRoot(0x0)/Pci(0x1,0x0)/Pci(0x0,0x0)"
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

	bootRestoreInterval time.Duration
	bootRestoreTimeout  time.Duration

	// mu guards the state a client can change at runtime.
	mu           sync.Mutex
	identity     Identity
	passwordHash string
	// pendingBoot is the armed one-time boot source, empty when none is armed.
	pendingBoot server.ComputerSystemBootSource
}

func NewVirtualMachineResourceManager(
	ctx context.Context,
	virtClient kvclient.Interface,
	cdiClient cdiclient.Interface,
) *VirtualMachineResourceManager {
	return &VirtualMachineResourceManager{
		ctx:                 ctx,
		virtClient:          virtClient,
		cdiClient:           cdiClient,
		bootRestoreInterval: defaultBootRestoreInterval,
		bootRestoreTimeout:  defaultBootRestoreTimeout,
	}
}

func (m *VirtualMachineResourceManager) Initialize(namespace, name string) error {
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(namespace).Get(m.ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	m.namespace = vm.Namespace
	m.name = vm.Name

	identity := identityFromAnnotations(vm.Annotations, string(vm.UID))

	m.mu.Lock()
	m.identity = identity
	m.passwordHash = vm.Annotations[annotationPasswordHash]
	m.mu.Unlock()

	// Detect boot mode from VM firmware configuration
	bootMode := m.detectBootMode(vm)

	// Initialize computer system
	m.computerSystem = NewComputerSystem(
		identity.SystemID,
		strings.Join([]string{vm.Namespace, vm.Name}, "/"),
		identity.SystemSerial,
		powerStateMap[vm.Status.Ready],
		bootMode,
	)

	if adapter, ok := m.computerSystem.(*ComputerSystemAdapter); ok {
		adapter.ComputerSystem().Manufacturer = util.Ptr(identity.Manufacturer)
		adapter.ComputerSystem().Model = util.Ptr(identity.Model)

		// A DPU advertises the NVIDIA OEM resource on its ComputerSystem so clients
		// follow the reference to read NIC mode and base MAC. The resource itself is
		// served by a custom route in the Redfish emulator.
		if !identity.DPU && identity.HostBootMAC != "" {
			adapter.ComputerSystem().Boot.BootOptions = server.OdataV4IdRef{
				OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/BootOptions", identity.SystemID),
			}
		}

		if identity.DPU {
			adapter.ComputerSystem().SecureBoot = server.OdataV4IdRef{
				OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/SecureBoot", identity.SystemID),
			}
			adapter.ComputerSystem().Oem = map[string]interface{}{
				"Nvidia": map[string]interface{}{
					"@odata.id": fmt.Sprintf("/redfish/v1/Systems/%s/Oem/Nvidia", identity.SystemID),
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
	m.manager = NewManager(identity.ManagerID, defaultManagerName)

	// Initialize virtual media
	m.virtualMedia = NewVirtualMedia(identity.ManagerID, defaultVirtualMediaId, defaultVirtualMediaName)

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

	return m.startWithBootOnce(func() error {
		return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Start(m.ctx, m.name, &kubevirtv1.StartOptions{})
	})
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

	return m.startWithBootOnce(func() error {
		return m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Restart(m.ctx, m.name, &kubevirtv1.RestartOptions{})
	})
}

// startWithBootOnce runs start, which restarts or starts the VM. When a one-time
// boot is armed it first points the VM spec's boot order at the armed source and,
// once start succeeded, schedules the original boot order to be restored as soon
// as the new VMI exists, so only this one boot follows the override.
func (m *VirtualMachineResourceManager) startWithBootOnce(start func() error) error {
	m.mu.Lock()
	target := m.pendingBoot
	m.pendingBoot = ""
	m.mu.Unlock()

	if target == "" {
		return start()
	}

	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VM for one-time boot: %w", err)
	}
	if vm.Spec.Template == nil {
		return fmt.Errorf("failed to apply one-time boot: no template found")
	}

	devices := &vm.Spec.Template.Spec.Domain.Devices
	saved := saveBootOrders(devices)
	if err := bootFirst(devices, target, m.Identity().OOBInterface); err != nil {
		return fmt.Errorf("failed to apply one-time boot: %w", err)
	}
	if _, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Update(m.ctx, vm, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to apply one-time boot: %w", err)
	}

	var oldUID types.UID
	vmi, err := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err == nil {
		oldUID = vmi.UID
	}

	if err := start(); err != nil {
		if restoreErr := m.restoreBootOrders(saved); restoreErr != nil {
			logrus.WithError(restoreErr).Error("Failed to restore boot order after a failed start")
		}
		m.mu.Lock()
		m.pendingBoot = target
		m.mu.Unlock()
		return err
	}

	m.computerSystem.ClearBootOverride()
	go m.restoreBootOrdersWhenRestarted(saved, oldUID)

	return nil
}

// restoreBootOrdersWhenRestarted waits for a VMI other than the one with oldUID,
// which has copied the one-time boot order from the VM spec, and then puts the
// original boot order back.
func (m *VirtualMachineResourceManager) restoreBootOrdersWhenRestarted(saved bootOrders, oldUID types.UID) {
	err := wait.PollUntilContextTimeout(m.ctx, m.bootRestoreInterval, m.bootRestoreTimeout, true,
		func(ctx context.Context) (bool, error) {
			vmi, err := m.virtClient.KubevirtV1().VirtualMachineInstances(m.namespace).
				Get(ctx, m.name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			return vmi.UID != oldUID, nil
		})
	if err != nil {
		logrus.WithError(err).Warn("VMI did not restart in time, restoring boot order anyway")
	}

	if err := m.restoreBootOrders(saved); err != nil {
		logrus.WithError(err).Error("Failed to restore boot order after one-time boot")
		return
	}
	logrus.Info("Restored boot order after one-time boot")
}

func (m *VirtualMachineResourceManager) restoreBootOrders(saved bootOrders) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Get(m.ctx, m.name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if vm.Spec.Template == nil {
			return fmt.Errorf("no template found")
		}

		saved.restore(&vm.Spec.Template.Spec.Domain.Devices)

		_, err = m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Update(m.ctx, vm, metav1.UpdateOptions{})
		return err
	})
}

// bootOrders is a snapshot of the boot order of every interface and disk, keyed
// by device name; a missing or nil entry means the device has no boot order.
type bootOrders struct {
	interfaces map[string]*uint
	disks      map[string]*uint
}

func saveBootOrders(devices *kubevirtv1.Devices) bootOrders {
	saved := bootOrders{
		interfaces: make(map[string]*uint, len(devices.Interfaces)),
		disks:      make(map[string]*uint, len(devices.Disks)),
	}
	for _, iface := range devices.Interfaces {
		saved.interfaces[iface.Name] = iface.BootOrder
	}
	for _, disk := range devices.Disks {
		saved.disks[disk.Name] = disk.BootOrder
	}
	return saved
}

func (b bootOrders) restore(devices *kubevirtv1.Devices) {
	for i := range devices.Interfaces {
		devices.Interfaces[i].BootOrder = b.interfaces[devices.Interfaces[i].Name]
	}
	for i := range devices.Disks {
		devices.Disks[i].BootOrder = b.disks[devices.Disks[i].Name]
	}
}

// bootFirst makes the device behind target boot first and the first other disk
// the fallback, clearing every other boot order. A network source resolves to the
// interface named oobInterface, or the first interface when there is none by
// that name.
func bootFirst(devices *kubevirtv1.Devices, target server.ComputerSystemBootSource, oobInterface string) error {
	var first, second uint = 1, 2

	var firstDisk *kubevirtv1.Disk
	switch target {
	case server.COMPUTERSYSTEMBOOTSOURCE_PXE, server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP:
		if len(devices.Interfaces) == 0 {
			return fmt.Errorf("no interfaces found")
		}
		idx := slices.IndexFunc(devices.Interfaces, func(i kubevirtv1.Interface) bool { return i.Name == oobInterface })
		if idx < 0 {
			idx = 0
		}
		clearBootOrders(devices)
		devices.Interfaces[idx].BootOrder = &first
	case server.COMPUTERSYSTEMBOOTSOURCE_CD:
		cdrom, err := util.GetCdromDisk(devices.Disks)
		if err != nil {
			return fmt.Errorf("no cdrom found: %w", err)
		}
		clearBootOrders(devices)
		cdrom.BootOrder = &first
		firstDisk = cdrom
	default:
		return fmt.Errorf("unsupported boot source: %s", target)
	}

	for i := range devices.Disks {
		if &devices.Disks[i] != firstDisk {
			devices.Disks[i].BootOrder = &second
			break
		}
	}

	return nil
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

func (m *VirtualMachineResourceManager) Identity() Identity {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.identity
}

func (m *VirtualMachineResourceManager) SetMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.identity.Mode = mode
}

func (m *VirtualMachineResourceManager) SetHostRshim(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.identity.HostRshim = hostRshimDisabled
	if enabled {
		m.identity.HostRshim = hostRshimEnabled
	}
}

func (m *VirtualMachineResourceManager) SetSecureBoot(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.identity.SecureBoot = enabled
}

func (m *VirtualMachineResourceManager) PasswordHash() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.passwordHash
}

// SetPasswordHash persists the hash on the VM, so a rotated password survives a
// restart of the BMC pod.
func (m *VirtualMachineResourceManager) SetPasswordHash(hash string) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Get(m.ctx, m.name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if vm.Annotations == nil {
			vm.Annotations = map[string]string{}
		}
		vm.Annotations[annotationPasswordHash] = hash

		_, err = m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
			Update(m.ctx, vm, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to store password hash: %w", err)
	}

	m.mu.Lock()
	m.passwordHash = hash
	m.mu.Unlock()

	return nil
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

	identity := m.Identity()

	running := vm.Status.Ready
	var linkStatus server.EthernetInterfaceV1120LinkStatus
	if running {
		linkStatus = server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_UP
	} else {
		linkStatus = server.ETHERNETINTERFACEV1120LINKSTATUS_LINK_DOWN
	}

	result := make([]EthernetInterfaceInterface, 0, len(interfaces)+1)
	for _, iface := range interfaces {
		if iface.MacAddress == "" {
			logrus.WithField("interface", iface.Name).Warn("Skipping interface without MAC address")
			continue
		}

		// NICo recognizes a DPU's OOB interface by "oob" in the Redfish id, whatever
		// the VM calls the interface.
		id := iface.Name
		if identity.DPU && iface.Name == identity.OOBInterface {
			id = oobEthernetInterfaceID
		}

		ethernetIface := NewEthernetInterface(
			identity.SystemID,
			id,
			iface.Name,
			iface.MacAddress,
			running,
			linkStatus,
		)
		result = append(result, ethernetIface)
	}

	// A host BMC synthesizes the boot NIC nico's ingestion keys on: an interface
	// whose MAC is the paired DPU's base MAC and that carries a UEFI/PCI device
	// path. The VM's real NICs (mgmt/fabric) do not match the DPU MAC, so nico
	// ignores them; this synthetic one is what sets the host's primary/boot
	// interface. Only when the boot MAC annotation is set (a DPU-backed host).
	if !identity.DPU && identity.HostBootMAC != "" {
		bootIface := NewEthernetInterface(
			identity.SystemID,
			hostBootInterfaceID,
			hostBootInterfaceID,
			identity.HostBootMAC,
			running,
			linkStatus,
		)
		bootIface.EthernetInterface().UefiDevicePath = util.Ptr(hostBootInterfaceUefiDevicePath)
		result = append(result, bootIface)
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

func (m *VirtualMachineResourceManager) SetBootOnce(target server.ComputerSystemBootSource) error {
	if m.computerSystem == nil {
		return fmt.Errorf("computer system not initialized")
	}

	if target == server.COMPUTERSYSTEMBOOTSOURCE_HDD || target == server.COMPUTERSYSTEMBOOTSOURCE_NONE {
		m.mu.Lock()
		m.pendingBoot = ""
		m.mu.Unlock()
		m.computerSystem.ClearBootOverride()
		return nil
	}

	// Resolve the target against the current VM spec now, so an override that
	// cannot be applied is rejected here instead of failing the later restart.
	vm, err := m.virtClient.KubevirtV1().VirtualMachines(m.namespace).
		Get(m.ctx, m.name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.Spec.Template == nil {
		return fmt.Errorf("no template found")
	}
	if err := bootFirst(&vm.Spec.Template.Spec.Domain.Devices, target, m.Identity().OOBInterface); err != nil {
		return err
	}

	m.mu.Lock()
	m.pendingBoot = target
	m.mu.Unlock()
	m.computerSystem.SetBootOverrideOnce(target)

	return nil
}

func clearBootOrders(devices *kubevirtv1.Devices) {
	for i := range devices.Interfaces {
		devices.Interfaces[i].BootOrder = nil
	}
	for i := range devices.Disks {
		devices.Disks[i].BootOrder = nil
	}
}
