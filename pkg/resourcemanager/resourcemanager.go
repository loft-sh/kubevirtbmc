package resourcemanager

import "kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"

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

	// SetBootDevice makes the device the persistent first boot device.
	SetBootDevice(BootDevice) error
	// SetBootOnce arms a one-time boot from the source: the next restart or
	// power-on boots it, the one after boots as before. Pxe and UefiHttp boot the
	// DPU's OOB interface, Cd the cdrom; Hdd and None cancel a pending one.
	SetBootOnce(server.ComputerSystemBootSource) error

	// Identity is the mocked machine identity sourced from VM annotations.
	Identity() Identity
	SetMode(mode string)
	SetHostRshim(enabled bool)
	SetSecureBoot(enabled bool)

	// PasswordHash is the hash of the BMC password set through Redfish, empty
	// while the factory password is still in effect.
	PasswordHash() string
	SetPasswordHash(hash string) error
}
