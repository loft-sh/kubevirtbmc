package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
)

// The handlers below serve the DPU mutations nico drives that the generated
// Redfish router either has no route for or only a fragile typed one. Each is
// registered directly in the emulator and answers 404 on a non-DPU BMC.

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// HandleSystemSettings applies the boot source override libredfish PATCHes to
// Systems/<id>/Settings, e.g. {"Boot":{"BootSourceOverrideTarget":"UefiHttp",
// "BootSourceOverrideEnabled":"Once"}}. A one-time override is stateful: it is
// consumed by the next restart or power-on (see ResourceManager.SetBootOnce).
func (h *handler) HandleSystemSettings(w http.ResponseWriter, r *http.Request) {
	if !h.rm.Identity().DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var body struct {
		Boot struct {
			Enabled server.ComputerSystemV1220BootSourceOverrideEnabled `json:"BootSourceOverrideEnabled"`
			Target  server.ComputerSystemBootSource                     `json:"BootSourceOverrideTarget"`
		} `json:"Boot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if body.Boot.Enabled == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.setBootOverride(body.Boot.Enabled, body.Boot.Target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleHostRshimSet records {"HostRshim":"Enabled|Disabled"}, which the OEM
// resource then reports back.
func (h *handler) HandleHostRshimSet(w http.ResponseWriter, r *http.Request) {
	if !h.rm.Identity().DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var body struct {
		HostRshim string `json:"HostRshim"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch body.HostRshim {
	case valueEnabled:
		h.rm.SetHostRshim(true)
	case "Disabled":
		h.rm.SetHostRshim(false)
	default:
		http.Error(w, fmt.Sprintf("invalid HostRshim %q", body.HostRshim), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleModeSet records {"Mode":"DpuMode|NicMode"}, which the OEM resource and the
// BIOS NicMode attribute then report back.
func (h *handler) HandleModeSet(w http.ResponseWriter, r *http.Request) {
	if !h.rm.Identity().DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var body struct {
		Mode string `json:"Mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if body.Mode != "DpuMode" && body.Mode != "NicMode" {
		http.Error(w, fmt.Sprintf("invalid Mode %q", body.Mode), http.StatusBadRequest)
		return
	}
	h.rm.SetMode(body.Mode)

	w.WriteHeader(http.StatusNoContent)
}

// HandleSecureBootPatch records {"SecureBootEnable":true|false}.
func (h *handler) HandleSecureBootPatch(w http.ResponseWriter, r *http.Request) {
	if !h.rm.Identity().DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var body struct {
		SecureBootEnable *bool `json:"SecureBootEnable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if body.SecureBootEnable != nil {
		h.rm.SetSecureBoot(*body.SecureBootEnable)
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleSecureBootCertificateAdd accepts a certificate for a Secure Boot database
// (nico enrolls the PK this way) and answers with a completed task. The shim
// keeps no key database.
func (h *handler) HandleSecureBootCertificateAdd(w http.ResponseWriter, r *http.Request) {
	if !h.rm.Identity().DPU {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusAccepted, h.completedTask(bfbTaskID))
}
