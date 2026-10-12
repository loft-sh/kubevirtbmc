package resourcemanager

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIdentityFromAnnotations_Defaults(t *testing.T) {
	t.Run("dpu mirrors the BlueField-3 reference", func(t *testing.T) {
		id := identityFromAnnotations(map[string]string{annotationDPU: annotationTrue}, "aaaa-bbbb")

		require.True(t, id.DPU)
		require.Equal(t, "Nvidia", id.Vendor)
		require.Equal(t, "Nvidia", id.Manufacturer)
		require.Equal(t, "BlueField-3 DPU", id.Model)
		require.Equal(t, "Bluefield", id.SystemID)
		require.Equal(t, "Bluefield_BMC", id.ManagerID)
		require.Equal(t, "aaaabbbb", id.SystemSerial)
		require.Equal(t, "DpuMode", id.Mode)
		require.Equal(t, "Disabled", id.HostRshim)
		require.Equal(t, "oob_net0", id.OOBInterface)
		require.Equal(t, "BF-25.10-20", id.BMCFirmware)
		require.Equal(t, "b83f:d203:0090:97a4", id.SysImageFirmware)
	})

	t.Run("host", func(t *testing.T) {
		id := identityFromAnnotations(nil, "aaaa-bbbb")

		require.False(t, id.DPU)
		require.Equal(t, "HPE", id.Vendor)
		require.Equal(t, "1", id.SystemID)
		require.Equal(t, "BMC", id.ManagerID)
		require.Empty(t, id.PCIeDeviceSerial)
	})
}

func TestIdentityFromAnnotations_EveryFieldIsOverridable(t *testing.T) {
	id := identityFromAnnotations(map[string]string{
		annotationDPU:                  annotationTrue,
		annotationVendor:               "V",
		annotationProduct:              "P",
		annotationManufacturer:         "M",
		annotationSystemModel:          "SM",
		annotationSystemID:             "SID",
		annotationManagerID:            "MID",
		annotationSystemSerial:         "SER",
		annotationDPUModel:             "DM",
		annotationPartNumber:           "PN",
		annotationNicMode:              "NicMode",
		annotationBaseMAC:              testBaseMAC,
		annotationPCIeDeviceSerial:     "PCS",
		annotationPCIeDevicePartNumber: "PCPN",
		annotationPCIeDeviceVendor:     "PCV",
		annotationBMCFirmwareVersion:   "f-bmc",
		annotationCECFirmwareVersion:   "f-cec",
		annotationNICFirmwareVersion:   "f-nic",
		annotationUEFIFirmwareVersion:  "f-uefi",
		annotationSysImageVersion:      "f-sys",
		annotationBSPFirmwareVersion:   "f-bsp",
		annotationSecureBoot:           annotationTrue,
		annotationBootMAC:              "02:00:00:00:00:04",
		annotationOOBInterface:         "mgmt0",
	}, "uid")

	require.Equal(t, Identity{
		DPU:                    true,
		Vendor:                 "V",
		Product:                "P",
		Manufacturer:           "M",
		Model:                  "SM",
		SystemID:               "SID",
		ManagerID:              "MID",
		SystemSerial:           "SER",
		DPUModel:               "DM",
		PartNumber:             "PN",
		Mode:                   "NicMode",
		BaseMAC:                testBaseMAC,
		HostRshim:              "Disabled",
		SecureBoot:             true,
		PCIeDeviceSerial:       "PCS",
		PCIeDevicePartNumber:   "PCPN",
		PCIeDeviceManufacturer: "PCV",
		BMCFirmware:            "f-bmc",
		CECFirmware:            "f-cec",
		NICFirmware:            "f-nic",
		UEFIFirmware:           "f-uefi",
		SysImageFirmware:       "f-sys",
		BSPFirmware:            "f-bsp",
		HostBootMAC:            "02:00:00:00:00:04",
		OOBInterface:           "mgmt0",
	}, id)
}

func TestSysImageFromBaseMAC(t *testing.T) {
	// NICo recovers the MAC by dropping the four zeros inserted after the third octet.
	require.Equal(t, "0200:0000:0000:0001", sysImageFromBaseMAC(testBaseMAC))
	require.Equal(t, "aabb:cc00:00dd:eeff", sysImageFromBaseMAC("aa:bb:cc:dd:ee:ff"))
	require.Equal(t, defaultSysImageVersion, sysImageFromBaseMAC(""))
	require.Equal(t, defaultSysImageVersion, sysImageFromBaseMAC("not-a-mac"))
}
