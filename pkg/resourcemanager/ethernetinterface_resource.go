package resourcemanager

import (
	"fmt"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
)

type EthernetInterfaceInterface interface {
	OdataInterface

	Id() string
}

type EthernetInterfaceAdapter struct {
	ethernetInterface *server.EthernetInterfaceV1120EthernetInterface
}

func NewEthernetInterface(systemID, id, name, macAddress string, enabled bool, linkStatus server.EthernetInterfaceV1120LinkStatus) *EthernetInterfaceAdapter {
	generatedEthernetInterface := &server.EthernetInterfaceV1120EthernetInterface{
		OdataContext:     "/redfish/v1/$metadata#EthernetInterface.EthernetInterface",
		OdataId:          fmt.Sprintf("/redfish/v1/Systems/%s/EthernetInterfaces/%s", systemID, id),
		OdataType:        "#EthernetInterface.v1_12_0.EthernetInterface",
		Id:               id,
		Name:             name,
		MACAddress:       macAddress,
		InterfaceEnabled: &enabled,
		LinkStatus:       linkStatus,
	}

	return &EthernetInterfaceAdapter{ethernetInterface: generatedEthernetInterface}
}

func (a *EthernetInterfaceAdapter) Id() string {
	return a.ethernetInterface.Id
}

func (a *EthernetInterfaceAdapter) OdataId() string {
	return a.ethernetInterface.OdataId
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
