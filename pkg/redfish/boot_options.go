package redfish

import (
	"fmt"

	"github.com/sirupsen/logrus"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

// Boot order is boot-OPTION based, not BootSourceOverride based. A client
// setting the boot order compares the first entry of Boot.BootOrder against the
// boot option it expects to boot from, matching on DisplayName, so all three of
// these have to line up: the BootOptions collection has to exist, the members
// have to be individually fetchable, and Boot.BootOrder has to name them.
const (
	bootOptionsPathTemplate = "/redfish/v1/Systems/%s/BootOptions"

	// BootOptionReference values follow the UEFI convention real firmware uses,
	// BootNNNN in hex, which is also what the reference Dell mock emits.
	httpBootOptionReference = "Boot0000"

	// httpBootDisplayNamePrefix is the literal a client prepends to the NIC's
	// device description to build the name it expects. Changing the served name
	// means changing the device description, via
	// BMC_NIC_DEVICE_DESCRIPTION, so that both ends move together.
	httpBootDisplayNamePrefix = "HTTP Device 1: "
)

// bootNICID is the id of the interface the host network-boots from, which is
// also the id its network device function must carry.
func (h *handler) bootNICID() string {
	return h.bootNIC().interfaceID
}

// httpBootDisplayName is the name the first boot order entry is matched on.
//
// Built from the SAME device description the NIC chain serves, which is the
// whole point: the client derives what it expects as
// "HTTP Device 1: " + DeviceDescription and compares it to this string for
// exact equality, tolerating only a trailing " - <suffix>". Deriving both from
// one value makes drift impossible; two independent constants would eventually
// disagree and the match could then never succeed.
func (h *handler) httpBootDisplayName() string {
	return httpBootDisplayNamePrefix + h.bootNIC().deviceDescription
}

// httpBootOption is the single boot option served.
//
// Deliberately one option, not a realistic boot order. Only the first entry is
// compared, so a longer list would be invented detail with more surface to be
// wrong about. It is a network HTTP boot entry because that is what these hosts
// actually boot: UEFI HTTP from the provisioning service.
func (h *handler) httpBootOption(computerSystemID string) *server.BootOptionV105BootOption {
	reference := httpBootOptionReference

	return &server.BootOptionV105BootOption{
		OdataContext:        "/redfish/v1/$metadata#BootOption.BootOption",
		OdataId:             fmt.Sprintf(bootOptionsPathTemplate, computerSystemID) + "/" + reference,
		OdataType:           "#BootOption.v1_0_5.BootOption",
		Id:                  reference,
		Name:                "Boot Option",
		Description:         "Boot Option",
		BootOptionReference: util.Ptr(reference),
		BootOptionEnabled:   util.Ptr(true),
		DisplayName:         util.Ptr(h.httpBootDisplayName()),
		// The interface this option boots from. Harmless if a client ignores
		// it, and it is the honest answer for a network boot entry.
		RelatedItem: []server.OdataV4IdRef{
			{
				OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/EthernetInterfaces/%s",
					computerSystemID, h.bootNICID()),
			},
		},
		RelatedItemodataCount: 1,
		Alias:                 server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP,
	}
}

// GetBootOptionCollection serves the boot options collection.
func (h *handler) GetBootOptionCollection(computerSystemID string) *server.BootOptionCollectionBootOptionCollection {
	members := []server.OdataV4IdRef{
		{
			OdataId: fmt.Sprintf(bootOptionsPathTemplate, computerSystemID) + "/" + httpBootOptionReference,
		},
	}

	return &server.BootOptionCollectionBootOptionCollection{
		OdataContext:      "/redfish/v1/$metadata#BootOptionCollection.BootOptionCollection",
		OdataId:           fmt.Sprintf(bootOptionsPathTemplate, computerSystemID),
		OdataType:         "#BootOptionCollection.BootOptionCollection",
		Name:              "Boot Option Collection",
		Description:       "Boot Option Collection",
		Members:           members,
		MembersodataCount: int64(len(members)),
	}
}

// GetBootOption serves one member. A reference that is not served is a 404
// rather than a fabricated option, so a client cannot be told a boot device
// exists when it does not.
func (h *handler) GetBootOption(computerSystemID, bootOptionID string) (*server.BootOptionV105BootOption, error) {
	if bootOptionID != httpBootOptionReference {
		return nil, fmt.Errorf("unknown boot option %q", bootOptionID)
	}

	return h.httpBootOption(computerSystemID), nil
}

// applyBootOrder fills in the parts of Boot that describe the boot order.
//
// The ComputerSystem is built before the interfaces are known, and Boot
// previously carried only the BootSourceOverride trio, with no BootOrder at
// all. A client that reads the first entry of BootOrder finds nothing to
// compare and cannot conclude the order is set, so the phase never completes.
func (h *handler) applyBootOrder(computerSystem *server.ComputerSystemV1220ComputerSystem) {
	if computerSystem == nil {
		return
	}

	computerSystem.Boot.BootOptions = server.OdataV4IdRef{
		OdataId: fmt.Sprintf(bootOptionsPathTemplate, computerSystem.Id),
	}

	// The HTTP boot entry is first because that is the entry the comparison
	// looks at.
	computerSystem.Boot.BootOrder = []*string{util.Ptr(httpBootOptionReference)}

	// Says which of the two boot-order mechanisms this system honours. Without
	// it a client cannot tell whether to trust BootOrder or the override trio.
	computerSystem.Boot.BootOrderPropertySelection = server.COMPUTERSYSTEMV1220BOOTORDERTYPES_BOOT_ORDER
}

// logBootConfiguration records what the boot surface reports, once at startup.
// A client that rejects the boot order says only that it did not match, never
// which name it saw, so the served name has to be greppable from this side.
func (h *handler) logBootConfiguration() {
	logrus.WithFields(logrus.Fields{
		"bootOptionReference": httpBootOptionReference,
		"displayName":         h.httpBootDisplayName(),
		"nic":                 h.bootNICID(),
	}).Info("Serving boot options")
}

// applyServedNavigationLinks brings the ComputerSystem's advertised links into
// line with what is actually served.
//
// An absent navigation property means "not supported" and is handled
// gracefully; an advertised one that answers 501 is fatal to a client that
// walks it. NetworkInterfaces, SimpleStorage and the System-anchored
// VirtualMedia were all advertised and all answer 501, so they are cleared. The
// Manager-anchored VirtualMedia is a different resource and is served, so the
// Manager keeps its link.
//
// SecureBoot is advertised here, now that the route serves it: the boot-order
// step reads secure boot state and will not proceed without it.
func (h *handler) applyServedNavigationLinks(computerSystem *server.ComputerSystemV1220ComputerSystem) {
	if computerSystem == nil {
		return
	}

	computerSystem.SecureBoot = server.OdataV4IdRef{
		OdataId: fmt.Sprintf("/redfish/v1/Systems/%s/SecureBoot", computerSystem.Id),
	}

	// Zero value serializes to nothing once empty objects are pruned, so these
	// disappear from the payload rather than going out as unreachable links.
	computerSystem.NetworkInterfaces = server.OdataV4IdRef{}
	computerSystem.SimpleStorage = server.OdataV4IdRef{}
	computerSystem.VirtualMedia = server.OdataV4IdRef{}

	// The chassis is served now, so the System may point at it.
	computerSystem.Links.Chassis = []server.OdataV4IdRef{
		{OdataId: fmt.Sprintf("/redfish/v1/Chassis/%s", h.chassisID())},
	}
}
