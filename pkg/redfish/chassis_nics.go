package redfish

import (
	"fmt"
	"os"
	"strings"

	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

// The chassis-anchored NIC chain.
//
// This exists for one reason: resolving the name of the host's HTTP boot
// option. A client derives the expected DisplayName as
//
//	"HTTP Device 1: " + <NIC's Oem.Dell.DellNIC.DeviceDescription>
//
// and the only path to that description runs through the chassis:
//
//	Chassis/{system_id} -> NetworkAdapters -> NetworkDeviceFunctions -> the NDF
//	whose Id matches the host interface -> Oem.Dell.DellNIC.DeviceDescription
//
// Every link in that chain is mandatory, each with its own distinct failure, so
// the whole chain is served or none of it is useful.
//
// The chassis id MUST equal the ComputerSystem id. A client fetches
// Chassis/{system_id} directly, so a chassis under any other id is unreachable
// no matter what the collection lists.
const (
	// NICDeviceDescriptionEnvVar overrides the NIC's reported device
	// description.
	//
	// This is the single source of truth for the boot option name: the expected
	// DisplayName is built from it, and the DisplayName this BMC serves is
	// built from the same value, so the two cannot drift. The match is
	// case-sensitive and exact, tolerating only a trailing " - <suffix>", so
	// there is no room for an approximation.
	NICDeviceDescriptionEnvVar = "BMC_NIC_DEVICE_DESCRIPTION"

	// defaultNICDeviceDescription follows the convention real Dell firmware
	// uses and the reference mock copies. The slot number is not meaningful
	// here -- there is no physical slot -- but the form is what a Dell host
	// reports, and the value only has to agree with itself.
	defaultNICDeviceDescription = "NIC in Slot 1 Port 1"

	chassisOdataType               = "#Chassis.v1_25_0.Chassis"
	networkAdapterOdataType        = "#NetworkAdapter.v1_9_0.NetworkAdapter"
	networkDeviceFunctionOdataType = "#NetworkDeviceFunction.v1_9_0.NetworkDeviceFunction"
)

// hostNIC is one host interface, and the chassis-side identifiers that describe
// it.
type hostNIC struct {
	// interfaceID is the EthernetInterface id. The NDF Id must equal it: that
	// is how a client matches an interface to its device function.
	interfaceID string
	macAddress  string
	// adapterID is the NetworkAdapter this function sits under. Its value is
	// arbitrary; nothing matches on it.
	adapterID string
	// deviceDescription is what the boot option name is built from.
	deviceDescription string
}

// hostNICs describes the host's interfaces, chassis-side.
//
// Falls back to a single synthetic NIC when the interfaces cannot be read, so
// that the chain stays coherent rather than serving a chassis with no adapters,
// which is one of the mandatory-key failures.
func (h *handler) hostNICs() []hostNIC {
	configuredDescription := strings.TrimSpace(os.Getenv(NICDeviceDescriptionEnvVar))
	if configuredDescription == "" {
		configuredDescription = defaultNICDeviceDescription
	}

	fallback := []hostNIC{
		{
			interfaceID:       "default",
			adapterID:         "NIC.Slot.1",
			deviceDescription: configuredDescription,
		},
	}

	if h.rm == nil {
		return fallback
	}

	interfaces, err := h.rm.GetEthernetInterfaces()
	if err != nil || len(interfaces) == 0 {
		return fallback
	}

	nics := make([]hostNIC, 0, len(interfaces))
	for i, iface := range interfaces {
		description := configuredDescription
		if i > 0 {
			// Only the first NIC's description feeds the boot option name, so
			// the rest are numbered off the same form rather than sharing a
			// value that would make two adapters indistinguishable.
			description = fmt.Sprintf("NIC in Slot %d Port 1", i+1)
		}

		nics = append(nics, hostNIC{
			interfaceID:       iface.Id(),
			macAddress:        iface.MACAddress(),
			adapterID:         fmt.Sprintf("NIC.Slot.%d", i+1),
			deviceDescription: description,
		})
	}

	return nics
}

// bootNIC is the interface the host network-boots from, and whose description
// the boot option is named after.
func (h *handler) bootNIC() hostNIC {
	return h.hostNICs()[0]
}

// chassisID is the id the chassis must be served under.
func (h *handler) chassisID() string {
	return resourcemanager.DefaultComputerSystemId
}

// GetChassis serves the chassis member.
//
// Power, Thermal, Sensors, PCIeDevices, Assembly and the rest are deliberately
// OMITTED rather than advertised. Adding a chassis member creates new surface
// for a client to walk, and an advertised link that answers 501 is fatal where
// an absent property means "not supported" and is handled gracefully. Only
// NetworkAdapters is advertised, because only NetworkAdapters is served.
func (h *handler) GetChassis(chassisID string) (map[string]any, error) {
	if chassisID != h.chassisID() {
		return nil, fmt.Errorf("unknown chassis %q", chassisID)
	}

	return map[string]any{
		"@odata.context": "/redfish/v1/$metadata#Chassis.Chassis",
		"@odata.id":      fmt.Sprintf("/redfish/v1/Chassis/%s", chassisID),
		"@odata.type":    chassisOdataType,
		"Id":             chassisID,
		"Name":           "Computer System Chassis",
		"Description":    "Computer System Chassis",
		// Required, and RackMount is the honest answer for a machine presented
		// as a rack server.
		"ChassisType":  "RackMount",
		"Manufacturer": "KubeVirt",
		"Model":        "KubeVirt",
		"Status": map[string]any{
			"Health": "OK",
			"State":  "Enabled",
		},
		"NetworkAdapters": map[string]any{
			"@odata.id": fmt.Sprintf("/redfish/v1/Chassis/%s/NetworkAdapters", chassisID),
		},
		"Links": map[string]any{
			"ComputerSystems": []any{
				map[string]any{
					"@odata.id": fmt.Sprintf("/redfish/v1/Systems/%s", resourcemanager.DefaultComputerSystemId),
				},
			},
			"ManagedBy": []any{
				map[string]any{
					"@odata.id": fmt.Sprintf("/redfish/v1/Managers/%s", resourcemanager.DefaultManagerId),
				},
			},
		},
	}, nil
}

// GetNetworkAdapterCollection lists the chassis's adapters.
func (h *handler) GetNetworkAdapterCollection(chassisID string) (map[string]any, error) {
	if chassisID != h.chassisID() {
		return nil, fmt.Errorf("unknown chassis %q", chassisID)
	}

	nics := h.hostNICs()
	members := make([]any, 0, len(nics))
	for _, nic := range nics {
		members = append(members, map[string]any{
			"@odata.id": fmt.Sprintf("/redfish/v1/Chassis/%s/NetworkAdapters/%s", chassisID, nic.adapterID),
		})
	}

	return map[string]any{
		"@odata.context":      "/redfish/v1/$metadata#NetworkAdapterCollection.NetworkAdapterCollection",
		"@odata.id":           fmt.Sprintf("/redfish/v1/Chassis/%s/NetworkAdapters", chassisID),
		"@odata.type":         "#NetworkAdapterCollection.NetworkAdapterCollection",
		"Name":                "Network Adapter Collection",
		"Members":             members,
		"Members@odata.count": len(members),
	}, nil
}

// GetNetworkAdapter serves one adapter.
//
// NetworkDeviceFunctions is the only navigation property advertised. An adapter
// that does not expose it is skipped by the client rather than being an error,
// so advertising it is what puts this adapter on the search path at all.
func (h *handler) GetNetworkAdapter(chassisID, adapterID string) (map[string]any, error) {
	nic, err := h.findNICByAdapter(chassisID, adapterID)
	if err != nil {
		return nil, err
	}

	base := fmt.Sprintf("/redfish/v1/Chassis/%s/NetworkAdapters/%s", chassisID, adapterID)

	return map[string]any{
		"@odata.context": "/redfish/v1/$metadata#NetworkAdapter.NetworkAdapter",
		"@odata.id":      base,
		"@odata.type":    networkAdapterOdataType,
		"Id":             adapterID,
		"Name":           nic.deviceDescription,
		"Description":    nic.deviceDescription,
		"Manufacturer":   "KubeVirt",
		"Model":          "VirtIO Network Adapter",
		"Status": map[string]any{
			"Health": "OK",
			"State":  "Enabled",
		},
		"NetworkDeviceFunctions": map[string]any{
			"@odata.id": base + "/NetworkDeviceFunctions",
		},
	}, nil
}

// GetNetworkDeviceFunctionCollection lists an adapter's device functions.
func (h *handler) GetNetworkDeviceFunctionCollection(chassisID, adapterID string) (map[string]any, error) {
	nic, err := h.findNICByAdapter(chassisID, adapterID)
	if err != nil {
		return nil, err
	}

	base := fmt.Sprintf("/redfish/v1/Chassis/%s/NetworkAdapters/%s/NetworkDeviceFunctions", chassisID, adapterID)

	return map[string]any{
		"@odata.context": "/redfish/v1/$metadata#NetworkDeviceFunctionCollection.NetworkDeviceFunctionCollection",
		"@odata.id":      base,
		"@odata.type":    "#NetworkDeviceFunctionCollection.NetworkDeviceFunctionCollection",
		"Name":           "Network Device Function Collection",
		"Members": []any{
			map[string]any{"@odata.id": base + "/" + nic.interfaceID},
		},
		"Members@odata.count": 1,
	}, nil
}

// GetNetworkDeviceFunction serves the device function that carries the NIC's
// device description.
//
// The Id must equal the host interface id: that equality is how a client
// matches an interface to its function. Oem.Dell.DellNIC.DeviceDescription is
// the value the boot option name is built from, and each missing layer of that
// nesting has its own distinct error, so all three levels are present.
func (h *handler) GetNetworkDeviceFunction(chassisID, adapterID, functionID string) (map[string]any, error) {
	nic, err := h.findNICByAdapter(chassisID, adapterID)
	if err != nil {
		return nil, err
	}

	if functionID != nic.interfaceID {
		return nil, fmt.Errorf("unknown network device function %q", functionID)
	}

	ethernet := map[string]any{
		"MACAddress": nic.macAddress,
	}
	if nic.macAddress == "" {
		// Omitted rather than reported empty: an empty MAC is worse than none,
		// since a client may match on it.
		ethernet = map[string]any{}
	}

	function := map[string]any{
		"@odata.context": "/redfish/v1/$metadata#NetworkDeviceFunction.NetworkDeviceFunction",
		"@odata.id": fmt.Sprintf("/redfish/v1/Chassis/%s/NetworkAdapters/%s/NetworkDeviceFunctions/%s",
			chassisID, adapterID, functionID),
		"@odata.type":    networkDeviceFunctionOdataType,
		"Id":             functionID,
		"Name":           nic.deviceDescription,
		"Description":    nic.deviceDescription,
		"DeviceEnabled":  true,
		"NetDevFuncType": "Ethernet",
		"Status": map[string]any{
			"Health": "OK",
			"State":  "Enabled",
		},
		"Oem": map[string]any{
			"Dell": map[string]any{
				"DellNIC": map[string]any{
					"@odata.type":         "#DellNIC.v1_4_0.DellNIC",
					"Id":                  functionID,
					"Name":                nic.deviceDescription,
					"DeviceDescription":   nic.deviceDescription,
					"PermanentMACAddress": nic.macAddress,
				},
			},
		},
	}

	if len(ethernet) > 0 {
		function["Ethernet"] = ethernet
	}

	return function, nil
}

func (h *handler) findNICByAdapter(chassisID, adapterID string) (hostNIC, error) {
	if chassisID != h.chassisID() {
		return hostNIC{}, fmt.Errorf("unknown chassis %q", chassisID)
	}

	for _, nic := range h.hostNICs() {
		if nic.adapterID == adapterID {
			return nic, nil
		}
	}

	return hostNIC{}, fmt.Errorf("unknown network adapter %q", adapterID)
}

// GetSecureBoot serves the secure boot state.
//
// Exploration tolerates a failure here, but the boot-order step does not: it
// reads secure boot state before it will set a boot order, and a 501 ends the
// phase. Served with @odata.context included, since that has been mandatory in
// every client model checked so far even where the schema calls it optional.
//
// Reported disabled rather than enabled: nothing here verifies signatures, so
// claiming secure boot is active would be a lie a caller might rely on when
// deciding what it is allowed to boot.
func (h *handler) GetSecureBoot(computerSystemID string) map[string]any {
	return map[string]any{
		"@odata.context":        "/redfish/v1/$metadata#SecureBoot.SecureBoot",
		"@odata.id":             fmt.Sprintf("/redfish/v1/Systems/%s/SecureBoot", computerSystemID),
		"@odata.type":           "#SecureBoot.v1_1_1.SecureBoot",
		"Id":                    "SecureBoot",
		"Name":                  "UEFI Secure Boot",
		"Description":           "UEFI Secure Boot",
		"SecureBootEnable":      false,
		"SecureBootCurrentBoot": "Disabled",
		"SecureBootMode":        "UserMode",
	}
}
