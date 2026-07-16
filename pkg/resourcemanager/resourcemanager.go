package resourcemanager

type BootDevice string

const (
	BootDevicePxe BootDevice = "Pxe"
	BootDeviceHdd BootDevice = "Hdd"
	BootDeviceCd  BootDevice = "Cd"
)

type ResourceManager interface {
	GetComputerSystem() (ComputerSystemInterface, error)
	GetManager() (ManagerInterface, error)
	GetVirtualMedia() (VirtualMediaInterface, error)
	GetEthernetInterfaces() ([]EthernetInterfaceInterface, error)

	EjectMedia() error
	InsertMedia(string) error
	GetPowerStatus() (bool, error)
	PowerOn() error
	PowerOff() error
	PowerCycle() error
	SetBootDevice(BootDevice) error

	// Mocked DPU/host identity sourced from VM annotations.
	IsDPU() bool
	DPUCard() (model string)
	SystemOEM() (nicMode, baseMAC string)
	AttachedDPUSerial() (serial string, ok bool)
	BMCFirmwareVersion() (version string, ok bool)
	CECFirmwareVersion() (version string, ok bool)
	NICFirmwareVersion() (version string, ok bool)
	// HostBootMAC is the MAC of the host's boot NIC. nico resolves the boot
	// interface MAC (from its own record or the served EthernetInterface) and
	// requires a matching HTTP/IPv4 BootOption; the shim reports it so the boot
	// option and boot order line up.
	HostBootMAC() (mac string, ok bool)
}
