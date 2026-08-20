package resourcemanager

import (
	"fmt"
	"time"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

type ManagerInterface interface {
	OdataInterface

	Id() string
	SetDateTime(time.Time)
}

type ManagerAdapter struct {
	manager *server.ManagerV1190Manager
}

// NewManager builds the Manager (BMC) resource. managerUUID is derived from the
// KubeVirt VM UID by the caller, so each VM's BMC reports a distinct, stable
// UUID; a constant one makes every BMC in a fleet indistinguishable to clients
// that key on it.
func NewManager(id, name, managerUUID string) *ManagerAdapter {
	generatedManager := &server.ManagerV1190Manager{
		OdataContext: "/redfish/v1/$metadata#Manager.Manager",
		OdataId:      fmt.Sprintf("/redfish/v1/Managers/%s", id),
		OdataType:    "#Manager.v1_19_2.Manager",
		Description:  "Manager",
		Name:         name,
		Id:           id,
		UUID:         managerUUID,
		Model:        util.Ptr("KubeVirtBMC"),
		Status:       server.ResourceStatus{},
		ManagerType:  "BMC",
		Links:        server.ManagerV1190Links{},
		Actions: server.ManagerV1190Actions{
			ManagerReset: server.ManagerV1190Reset{
				Target: fmt.Sprintf("/redfish/v1/Managers/%s/Actions/Manager.Reset", id),
				Title:  "Reset",
			},
		},
		DateTime:            util.Ptr(time.Now().UTC()),
		DateTimeLocalOffset: util.Ptr("+00:00"),
		EthernetInterfaces: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/EthernetInterfaces", id),
		},
		LogServices: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/LogServices", id),
		},
		SerialInterfaces: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/SerialInterfaces", id),
		},
		VirtualMedia: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/VirtualMedia", id),
		},
	}

	return &ManagerAdapter{manager: generatedManager}
}

// SetDateTime refreshes the clock the BMC reports. It must be called on every
// read of the Manager: NewManager runs once at Initialize, so without this the
// BMC reports its pod start time forever and clients that check BMC clock drift
// (ironic, NICo) see the drift grow without bound.
func (a *ManagerAdapter) SetDateTime(t time.Time) {
	a.manager.DateTime = util.Ptr(t.UTC())
}

func (a *ManagerAdapter) Id() string {
	return a.manager.Id
}

func (a *ManagerAdapter) OdataId() string {
	return a.manager.OdataId
}

func (a *ManagerAdapter) Manage(resource OdataInterface) error {
	a.manager.Links.ManagerForServers = append(a.manager.Links.ManagerForServers, server.OdataV4IdRef{
		OdataId: resource.OdataId(),
	})

	return nil
}

func (a *ManagerAdapter) ManagedBy(resource OdataInterface) error {
	panic("implement me")
}

func (a *ManagerAdapter) Manager() *server.ManagerV1190Manager {
	return a.manager
}
