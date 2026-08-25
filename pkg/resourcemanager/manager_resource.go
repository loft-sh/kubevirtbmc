package resourcemanager

import (
	"fmt"
	"time"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

// DefaultManagerId is the id of the single Manager virtbmc serves. It is
// exported because OEM routes are built from it: the Dell attributes URI is
// derived from the Manager's id rather than being a fixed path.
const DefaultManagerId = "BMC"

// SerialNumberEnvVar names the environment variable that overrides the
// ComputerSystem serial number. Left unset, the serial is derived from the
// KubeVirt VM UID, which is unique and stable but cannot match a serial the
// consuming inventory was seeded with.
const SerialNumberEnvVar = "BMC_SERIAL_NUMBER"

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
		// Status was left zero-valued, which used to go out as `"Status":{}`.
		// Now that empty objects are dropped from the response it would vanish
		// instead, and a BMC that reports no health at all reads as a BMC in
		// trouble to anything that inventories one.
		Status: server.ResourceStatus{
			Health: util.Ptr(server.RESOURCEHEALTH_OK),
			State:  util.Ptr(server.RESOURCESTATE_ENABLED),
		},
		ManagerType: "BMC",
		Links:       server.ManagerV1190Links{},
		Actions: server.ManagerV1190Actions{
			ManagerReset: server.ManagerV1190Reset{
				Target: fmt.Sprintf("/redfish/v1/Managers/%s/Actions/Manager.Reset", id),
				Title:  "Reset",
			},
		},
		DateTime:            util.Ptr(formatBMCDateTime(time.Now())),
		DateTimeLocalOffset: util.Ptr("+00:00"),
		EthernetInterfaces: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/EthernetInterfaces", id),
		},
		LogServices: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/LogServices", id),
		},
		// Now served, so it must be advertised. It was previously left unset,
		// which meant it went out as `{}` and then, once empty objects started
		// being dropped, vanished entirely. A machine controller reads
		// IPMI.ProtocolEnabled from here as the very first step on a new host.
		NetworkProtocol: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/NetworkProtocol", id),
		},
		SerialInterfaces: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/SerialInterfaces", id),
		},
		VirtualMedia: server.OdataV4IdRef{
			OdataId: fmt.Sprintf("/redfish/v1/Managers/%s/VirtualMedia", id),
		},
		Oem: dellOem(id),
	}

	return &ManagerAdapter{manager: generatedManager}
}

// SetDateTime refreshes the clock the BMC reports. It must be called on every
// read of the Manager: NewManager runs once at Initialize, so without this the
// BMC reports its pod start time forever and clients that check BMC clock drift
// (ironic, NICo) see the drift grow without bound.
func (a *ManagerAdapter) SetDateTime(t time.Time) {
	a.manager.DateTime = util.Ptr(formatBMCDateTime(t))
}

// bmcDateTimeLayout is how a BMC reports its clock: a literal `+00:00` offset
// and whole seconds. Not RFC3339Nano, which is what encoding/json produces for
// a time.Time and which would report `Z` and six fractional digits.
const bmcDateTimeLayout = "2006-01-02T15:04:05+00:00"

func formatBMCDateTime(t time.Time) string {
	return t.UTC().Format(bmcDateTimeLayout)
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

// dellOem is the marker that makes a client look for Dell iDRAC attributes.
//
// nv-redfish issues the GET for /Oem/Dell/DellAttributes/<id> only when
// Manager.Oem.Dell exists. It is a bare presence check on the key and nothing
// inside is inspected, so `{"Dell": {}}` would be enough for exploration; an
// absent key yields Ok(None), which the lockdown check then turns into a hard
// error rather than a diff.
//
// The DelliDRACCard block is mandatory, not decoration, and the asymmetry
// between the two clients is the trap: nv-redfish only checks that the `Dell`
// key exists and never looks inside, while libredfish never checks for the key
// but fully deserializes what is there, into a struct whose every field is a
// bare String rather than an Option. So `{"Dell": {}}` passes exploration and
// then fails libredfish's get_manager().
//
// That is reachable rather than theoretical: get_manager() backs the BMC
// time-sync check that runs every preingestion tick, and failing that check is
// the entry point to the power-off / BMC-reset / wait remediation sequence. A
// half-populated block here would reintroduce that loop by a different route.
//
// Every value must therefore be present and non-empty. None is dereferenced,
// and the two timestamps are typed as strings rather than parsed, so the
// reference's literal values are fine to carry as-is.
func dellOem(managerID string) map[string]interface{} {
	return map[string]interface{}{
		"Dell": map[string]interface{}{
			"DelliDRACCard": map[string]interface{}{
				"@odata.context": "/redfish/v1/$metadata#DelliDRACCard.DelliDRACCard",
				"@odata.id": fmt.Sprintf(
					"/redfish/v1/Managers/%s/Oem/Dell/DelliDRACCard/%s-1_0x23_IDRACinfo",
					managerID, managerID,
				),
				"@odata.type":             "#DelliDRACCard.v1_1_0.DelliDRACCard",
				"Description":             "An instance of DelliDRACCard will have data specific to the Integrated Dell Remote Access Controller (iDRAC) in the managed system.",
				"IPMIVersion":             "2.0",
				"Id":                      fmt.Sprintf("%s-1_0x23_IDRACinfo", managerID),
				"LastSystemInventoryTime": "2026-02-20T04:38:38+00:00",
				"LastUpdateTime":          "2026-03-06T04:44:21+00:00",
				"Name":                    "DelliDRACCard",
				// The BMC's own address is not known here, and nothing
				// dereferences this. It is typed as a plain string, so it only
				// has to be present and non-empty.
				"URLString": "https://0.0.0.0:443",
			},
		},
	}
}
