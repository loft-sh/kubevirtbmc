package resourcemanager

import (
	"fmt"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

type EthernetInterfaceInterface interface {
	OdataInterface

	Id() string
	MACAddress() string
}

type EthernetInterfaceAdapter struct {
	ethernetInterface *server.EthernetInterfaceV1120EthernetInterface
}

// NewEthernetInterface builds the Redfish EthernetInterface resource backing a
// single KubeVirt VM network interface. The resource is anchored under the
// ComputerSystem, which is the owner Redfish assigns to host NICs.
func NewEthernetInterface(
	id, name, macAddress string,
	enabled bool,
	linkStatus server.EthernetInterfaceV1120LinkStatus,
) *EthernetInterfaceAdapter {
	return newEthernetInterface(
		fmt.Sprintf("/redfish/v1/Systems/1/EthernetInterfaces/%s", id),
		id, name, macAddress, enabled, linkStatus,
	)
}

// NewManagerEthernetInterface builds the same resource anchored under the
// Manager instead of the ComputerSystem. Redfish models the BMC's own NICs
// under /redfish/v1/Managers/{id}/EthernetInterfaces, and that is the path
// clients walk when they want the BMC MAC rather than the host MAC.
func NewManagerEthernetInterface(
	managerID string,
	id, name, macAddress string,
	enabled bool,
	linkStatus server.EthernetInterfaceV1120LinkStatus,
) *EthernetInterfaceAdapter {
	return newEthernetInterface(
		fmt.Sprintf("/redfish/v1/Managers/%s/EthernetInterfaces/%s", managerID, id),
		id, name, macAddress, enabled, linkStatus,
	)
}

func newEthernetInterface(
	odataID string,
	id, name, macAddress string,
	enabled bool,
	linkStatus server.EthernetInterfaceV1120LinkStatus,
) *EthernetInterfaceAdapter {
	health := server.RESOURCEHEALTH_OK
	state := server.RESOURCESTATE_ENABLED
	if !enabled {
		state = server.RESOURCESTATE_DISABLED
	}

	generatedEthernetInterface := &server.EthernetInterfaceV1120EthernetInterface{
		OdataContext:        "/redfish/v1/$metadata#EthernetInterface.EthernetInterface",
		OdataId:             odataID,
		OdataType:           "#EthernetInterface.v1_12_0.EthernetInterface",
		Description:         "Ethernet Interface",
		Name:                name,
		Id:                  id,
		MACAddress:          macAddress,
		PermanentMACAddress: macAddress,
		InterfaceEnabled:    util.Ptr(enabled),
		LinkStatus:          linkStatus,
		Status: server.ResourceStatus{
			Health: &health,
			State:  &state,
		},
	}

	return &EthernetInterfaceAdapter{ethernetInterface: generatedEthernetInterface}
}

func (a *EthernetInterfaceAdapter) Id() string {
	return a.ethernetInterface.Id
}

func (a *EthernetInterfaceAdapter) OdataId() string {
	return a.ethernetInterface.OdataId
}

func (a *EthernetInterfaceAdapter) MACAddress() string {
	return a.ethernetInterface.MACAddress
}

func (a *EthernetInterfaceAdapter) Manage(resource OdataInterface) error {
	panic("implement me")
}

func (a *EthernetInterfaceAdapter) ManagedBy(resource OdataInterface) error {
	panic("implement me")
}

func (a *EthernetInterfaceAdapter) EthernetInterface() *server.EthernetInterfaceV1120EthernetInterface {
	return a.ethernetInterface
}
