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
}
