package resourcemanager

import (
	"fmt"
	"strings"
)

// Annotation keys, all on the KubeVirt VM, that drive the mocked Redfish identity.
// Each is optional; an absent key falls back to the default below.
const (
	annotationPrefix = "bmc.kubevirtbmc.io/"
	annotationTrue   = "true"

	annotationDPU                  = annotationPrefix + "dpu"
	annotationVendor               = annotationPrefix + "vendor"
	annotationProduct              = annotationPrefix + "product"
	annotationManufacturer         = annotationPrefix + "manufacturer"
	annotationSystemModel          = annotationPrefix + "system-model"
	annotationSystemID             = annotationPrefix + "system-id"
	annotationManagerID            = annotationPrefix + "manager-id"
	annotationSystemSerial         = annotationPrefix + "system-serial"
	annotationDPUModel             = annotationPrefix + "dpu-model"
	annotationPartNumber           = annotationPrefix + "part-number"
	annotationNicMode              = annotationPrefix + "nic-mode"
	annotationBaseMAC              = annotationPrefix + "base-mac"
	annotationPCIeDeviceSerial     = annotationPrefix + "pcie-device-serial"
	annotationPCIeDevicePartNumber = annotationPrefix + "pcie-device-part-number"
	annotationPCIeDeviceVendor     = annotationPrefix + "pcie-device-manufacturer"
	annotationBMCFirmwareVersion   = annotationPrefix + "firmware-version"
	annotationCECFirmwareVersion   = annotationPrefix + "firmware-version-cec"
	annotationNICFirmwareVersion   = annotationPrefix + "firmware-version-nic"
	annotationUEFIFirmwareVersion  = annotationPrefix + "firmware-version-uefi"
	annotationSysImageVersion      = annotationPrefix + "firmware-version-sys-image"
	annotationBSPFirmwareVersion   = annotationPrefix + "firmware-version-bsp"
	annotationSecureBoot           = annotationPrefix + "secure-boot"
	annotationBootMAC              = annotationPrefix + "boot-mac"
	annotationOOBInterface         = annotationPrefix + "oob-interface"

	// annotationPasswordHash is written by the emulator itself when a client
	// rotates the BMC password (see SetPasswordHash).
	annotationPasswordHash = annotationPrefix + "password-hash"
)

// Defaults mirror NICo's BlueField-3 reference mock (crates/bmc-mock). The
// firmware versions equal NICo's hardware_models defaults so its exact-match
// firmware checks pass out of the box. The BMC version must stay in the
// "BF-XX.YY-Z" form: NICo gates its NIC mode query on a version_compare against
// "BF-23.10-5", and "BF-25.10-20" also passes the BFB install gate (>= 24.10).
const (
	defaultDPUVendor       = "Nvidia"
	defaultHostVendor      = "HPE"
	defaultDPUManufacturer = "Nvidia"
	defaultHostMaker       = "KubeVirt"
	defaultDPUModel        = "BlueField-3 DPU"
	defaultHostModel       = "KubeVirt"
	defaultDPUPartNumber   = "900-9D3B6-00CV-AA0"
	defaultPCIeDeviceMaker = "NVIDIA"
	defaultOOBInterface    = "oob_net0"

	defaultBMCFirmwareVersion  = "BF-25.10-20"
	defaultCECFirmwareVersion  = "00.02.0195.0000_n02"
	defaultNICFirmwareVersion  = "32.47.2682"
	defaultUEFIFirmwareVersion = "4.13.2-12-g943a91640d"
	defaultBSPFirmwareVersion  = "4.5.0.12984"
	defaultSysImageVersion     = "b83f:d203:0090:97a4"

	modeDPU = "DpuMode"
	modeNIC = "NicMode"

	hostRshimEnabled  = "Enabled"
	hostRshimDisabled = "Disabled"
)

// Identity is everything the emulator reports about the machine it fronts. Every
// field comes from a VM annotation, falling back to a default when unset, so
// handlers carry no identity constants of their own.
type Identity struct {
	// DPU selects the BlueField presentation; a non-DPU VM is a plain host BMC.
	DPU bool

	Vendor       string
	Product      string
	Manufacturer string
	Model        string
	SystemID     string
	ManagerID    string

	// SystemSerial is the ComputerSystem serial, the key NICo matches a host's
	// PCIe device against. It falls back to the VM UID without dashes.
	SystemSerial string

	// DPUModel and PartNumber describe the Card1 chassis of a DPU.
	DPUModel   string
	PartNumber string

	// Mode ("DpuMode" or "NicMode"), HostRshim ("Enabled" or "Disabled") and
	// SecureBoot are seeded from annotations and change at runtime through
	// Mode.Set, HostRshim.Set and a SecureBoot PATCH. BaseMAC is the pf0 MAC with
	// colons.
	Mode       string
	BaseMAC    string
	HostRshim  string
	SecureBoot bool

	// PCIeDeviceSerial is non-empty on a host that has a DPU attached; the part
	// number and manufacturer describe that PCIe device.
	PCIeDeviceSerial       string
	PCIeDevicePartNumber   string
	PCIeDeviceManufacturer string

	BMCFirmware      string
	CECFirmware      string
	NICFirmware      string
	UEFIFirmware     string
	SysImageFirmware string
	BSPFirmware      string

	// HostBootMAC is the MAC of the host's boot NIC; empty when unset.
	HostBootMAC string

	// OOBInterface names the VM interface that acts as the DPU's OOB NIC: it is
	// served as the Redfish EthernetInterface "oob_net0" and is the target of a
	// network boot override.
	OOBInterface string
}

func identityFromAnnotations(annotations map[string]string, vmUID string) Identity {
	var id Identity

	id.DPU = annotations[annotationDPU] == annotationTrue
	id.SecureBoot = annotations[annotationSecureBoot] == annotationTrue
	id.SystemSerial = valueOr(annotations[annotationSystemSerial], strings.ReplaceAll(vmUID, "-", ""))
	id.BaseMAC = annotations[annotationBaseMAC]
	id.PCIeDeviceSerial = annotations[annotationPCIeDeviceSerial]
	id.HostBootMAC = annotations[annotationBootMAC]

	id.Vendor = valueOr(annotations[annotationVendor], defaultHostVendor)
	id.Product = annotations[annotationProduct]
	id.Manufacturer = valueOr(annotations[annotationManufacturer], defaultHostMaker)
	id.Model = valueOr(annotations[annotationSystemModel], defaultHostModel)
	id.SystemID = valueOr(annotations[annotationSystemID], defaultComputerSystemId)
	id.ManagerID = valueOr(annotations[annotationManagerID], defaultManagerId)
	id.Mode = modeDPU
	id.HostRshim = hostRshimDisabled
	if id.DPU {
		id.Vendor = valueOr(annotations[annotationVendor], defaultDPUVendor)
		id.Product = valueOr(annotations[annotationProduct], defaultDPUModel)
		id.Manufacturer = valueOr(annotations[annotationManufacturer], defaultDPUManufacturer)
		id.Model = valueOr(annotations[annotationSystemModel], valueOr(annotations[annotationDPUModel], defaultDPUModel))
		id.SystemID = valueOr(annotations[annotationSystemID], bluefieldComputerSystemId)
		id.ManagerID = valueOr(annotations[annotationManagerID], bluefieldManagerId)
		if strings.EqualFold(annotations[annotationNicMode], "nic") || annotations[annotationNicMode] == modeNIC {
			id.Mode = modeNIC
		}
	}

	id.DPUModel = valueOr(annotations[annotationDPUModel], defaultDPUModel)
	id.PartNumber = valueOr(annotations[annotationPartNumber], defaultDPUPartNumber)

	id.PCIeDevicePartNumber = valueOr(annotations[annotationPCIeDevicePartNumber], defaultDPUPartNumber)
	id.PCIeDeviceManufacturer = valueOr(annotations[annotationPCIeDeviceVendor], defaultPCIeDeviceMaker)

	id.BMCFirmware = valueOr(annotations[annotationBMCFirmwareVersion], defaultBMCFirmwareVersion)
	id.CECFirmware = valueOr(annotations[annotationCECFirmwareVersion], defaultCECFirmwareVersion)
	id.NICFirmware = valueOr(annotations[annotationNICFirmwareVersion], defaultNICFirmwareVersion)
	id.UEFIFirmware = valueOr(annotations[annotationUEFIFirmwareVersion], defaultUEFIFirmwareVersion)
	id.BSPFirmware = valueOr(annotations[annotationBSPFirmwareVersion], defaultBSPFirmwareVersion)
	id.SysImageFirmware = valueOr(annotations[annotationSysImageVersion], sysImageFromBaseMAC(id.BaseMAC))

	id.OOBInterface = valueOr(annotations[annotationOOBInterface], defaultOOBInterface)

	return id
}

// sysImageFromBaseMAC derives the DPU_SYS_IMAGE version the way a BlueField
// does: the base MAC's hex digits with four zeros inserted after the third
// octet and regrouped as aabb:cc00:00dd:eeff. NICo recovers the MAC by dropping
// those four digits again.
func sysImageFromBaseMAC(baseMAC string) string {
	mac := strings.ReplaceAll(baseMAC, ":", "")
	if len(mac) != 12 {
		return defaultSysImageVersion
	}

	return fmt.Sprintf("%s:%s00:00%s:%s", mac[0:4], mac[4:6], mac[6:8], mac[8:12])
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
