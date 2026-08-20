package redfish

import (
	"fmt"
	"sync"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/util"
)

// Ports the resource advertises. Redfish reports the well-known port for each
// protocol rather than whatever the pod happens to listen on, because a client
// reads these to decide where to connect from outside the cluster, where the
// service publishes the standard ones.
const (
	ipmiPort  int64 = 623
	ntpPort   int64 = 123
	httpPort  int64 = 80
	httpsPort int64 = 443
	sshPort   int64 = 22
)

// networkProtocolState is the mutable part of ManagerNetworkProtocol.
//
// The resource is writable, so it cannot be rebuilt from constants on every
// read: a client that PATCHes NTP servers and reads them back has to see what
// it set. Guarded because reads and writes arrive on different connections.
type networkProtocolState struct {
	mu sync.RWMutex

	ipmiEnabled bool
	ntpEnabled  bool
	ntpServers  []string
}

// newNetworkProtocolState starts with IPMI-over-LAN reported as enabled.
//
// This is the first thing a machine controller checks on a new host, and it
// gates every later step: it reads IPMI.ProtocolEnabled and, if false, PATCHes
// it to true before doing anything else. Starting enabled means the check
// passes on the first read and the PATCH never has to fire.
//
// Note this reports on IPMI-over-LAN as a BMC feature, which is what the
// client is asking about, and not on whether this particular agent has its own
// IPMI simulator running -- that is off by default and is a separate concern
// from what the emulated BMC advertises.
func newNetworkProtocolState() *networkProtocolState {
	return &networkProtocolState{
		ipmiEnabled: true,
		ntpEnabled:  true,
		ntpServers:  []string{},
	}
}

// snapshot returns the mutable values under one lock, so a payload cannot be
// built from a half-applied PATCH.
func (s *networkProtocolState) snapshot() (ipmiEnabled, ntpEnabled bool, ntpServers []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	servers := make([]string, len(s.ntpServers))
	copy(servers, s.ntpServers)

	return s.ipmiEnabled, s.ntpEnabled, servers
}

// applyPatch merges a partial ManagerNetworkProtocol. Only the properties the
// body actually carries are touched: a nil pointer means "not mentioned", not
// "set to false".
func (s *networkProtocolState) applyPatch(patch server.ManagerNetworkProtocolV1100ManagerNetworkProtocol) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if patch.IPMI.ProtocolEnabled != nil {
		s.ipmiEnabled = *patch.IPMI.ProtocolEnabled
	}

	if patch.NTP.ProtocolEnabled != nil {
		s.ntpEnabled = *patch.NTP.ProtocolEnabled
	}

	// A present-but-empty list is a real instruction: it clears the servers.
	// Only a nil slice means the property was absent.
	if patch.NTP.NTPServers != nil {
		servers := make([]string, 0, len(patch.NTP.NTPServers))
		for _, server := range patch.NTP.NTPServers {
			if server != nil {
				servers = append(servers, *server)
			}
		}
		s.ntpServers = servers
	}
}

// GetManagerNetworkProtocol serves the manager's network protocol settings.
//
// The IPMI object and its ProtocolEnabled field are both mandatory: a client
// reading IPMI-over-LAN state treats a missing IPMI object and a missing
// ProtocolEnabled exactly like a failed request, not like "disabled", so
// omitting either strands the host on its first step rather than degrading.
//
// Every other protocol is reported too, with an honest ProtocolEnabled, rather
// than reporting only IPMI. Real BMCs enumerate their protocols, and a client
// whose model types any of these as non-optional fails on a payload that
// carries only the one property this caller happened to need.
func (h *handler) GetManagerNetworkProtocol(managerID string) *server.ManagerNetworkProtocolV1100ManagerNetworkProtocol {
	ipmiEnabled, ntpEnabled, ntpServers := h.networkProtocol.snapshot()

	servers := make([]*string, 0, len(ntpServers))
	for _, s := range ntpServers {
		servers = append(servers, util.Ptr(s))
	}

	return &server.ManagerNetworkProtocolV1100ManagerNetworkProtocol{
		OdataContext: "/redfish/v1/$metadata#ManagerNetworkProtocol.ManagerNetworkProtocol",
		OdataId:      fmt.Sprintf("/redfish/v1/Managers/%s/NetworkProtocol", managerID),
		OdataType:    "#ManagerNetworkProtocol.v1_10_0.ManagerNetworkProtocol",
		Id:           "NetworkProtocol",
		Name:         "Manager Network Protocol",
		Description:  "Manager Network Protocol",
		HostName:     util.Ptr(managerID),
		Status: server.ResourceStatus{
			Health: util.Ptr(server.RESOURCEHEALTH_OK),
			State:  util.Ptr(server.RESOURCESTATE_ENABLED),
		},

		// The property this whole resource exists to serve.
		IPMI: server.ManagerNetworkProtocolV1100Protocol{
			ProtocolEnabled: util.Ptr(ipmiEnabled),
			Port:            util.Ptr(ipmiPort),
		},

		// Writable: preingestion sets the time source here. NTPServers is
		// serialized even when empty, so "no servers configured" is
		// distinguishable from "does not report NTP".
		NTP: server.ManagerNetworkProtocolV1100NtpProtocol{
			ProtocolEnabled: util.Ptr(ntpEnabled),
			Port:            util.Ptr(ntpPort),
			NTPServers:      servers,
		},

		// Served by the emulator itself.
		HTTP: server.ManagerNetworkProtocolV1100Protocol{
			ProtocolEnabled: util.Ptr(true),
			Port:            util.Ptr(httpPort),
		},
		HTTPS: server.ManagerNetworkProtocolV1100HttpsProtocol{
			ProtocolEnabled: util.Ptr(true),
			Port:            util.Ptr(httpsPort),
		},
		VirtualMedia: server.ManagerNetworkProtocolV1100Protocol{
			ProtocolEnabled: util.Ptr(true),
		},

		// Reported as present and disabled rather than omitted. Omitting them
		// would read as "unsupported"; reporting them false is both true and
		// safe, since nothing should try to connect.
		SSH: server.ManagerNetworkProtocolV1100Protocol{
			ProtocolEnabled: util.Ptr(false),
			Port:            util.Ptr(sshPort),
		},
		Telnet: server.ManagerNetworkProtocolV1100Protocol{
			ProtocolEnabled: util.Ptr(false),
		},
		KVMIP: server.ManagerNetworkProtocolV1100Protocol{
			ProtocolEnabled: util.Ptr(false),
		},
	}
}

// PatchManagerNetworkProtocol applies a partial update and returns the result.
//
// Accepting this matters even though IPMI is already reported enabled: the same
// resource is where NTP servers get written, and that caller retries a few
// times and then gives up, which would leave the BMC with no time source and
// walk back into the clock-drift check that the just-in-time DateTime refresh
// was added to fix.
func (h *handler) PatchManagerNetworkProtocol(
	managerID string,
	patch server.ManagerNetworkProtocolV1100ManagerNetworkProtocol,
) *server.ManagerNetworkProtocolV1100ManagerNetworkProtocol {
	h.networkProtocol.applyPatch(patch)

	return h.GetManagerNetworkProtocol(managerID)
}
