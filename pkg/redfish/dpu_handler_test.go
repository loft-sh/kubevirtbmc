package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

func TestServiceRootVendor(t *testing.T) {
	// The vendor is what the annotation configured: nv-redfish needs exactly
	// "Nvidia" on a DPU to classify it as a BlueField.
	h, _ := newTestHandler(t, testDPUIdentity)
	root := h.GetServiceRoot()
	assert.Equal(t, "Nvidia", *root.Vendor)
	assert.Equal(t, "BlueField-3 DPU", *root.Product)
	assert.Equal(t, "/redfish/v1/Managers/Bluefield_BMC", root.Links.ManagerProvidingService.OdataId)

	h, _ = newTestHandler(t, testHostIdentity)
	root = h.GetServiceRoot()
	assert.Equal(t, "HPE", *root.Vendor)
	assert.Nil(t, root.Product)
}

func TestGetChassisCollectionDPU(t *testing.T) {
	h, _ := newTestHandler(t, testDPUIdentity)
	dpu := h.GetChassisCollection()
	assert.Equal(t, int64(2), dpu.MembersodataCount)
	assert.Equal(t, "/redfish/v1/Chassis/Card1", dpu.Members[1].OdataId)

	h, _ = newTestHandler(t, testHostIdentity)
	assert.Equal(t, int64(1), h.GetChassisCollection().MembersodataCount)
}

func TestGetComputerSystemCollectionSystemID(t *testing.T) {
	h, _ := newTestHandler(t, testDPUIdentity)
	assert.Equal(t, "/redfish/v1/Systems/Bluefield", h.GetComputerSystemCollection().Members[0].OdataId)

	h, _ = newTestHandler(t, testHostIdentity)
	assert.Equal(t, "/redfish/v1/Systems/1", h.GetComputerSystemCollection().Members[0].OdataId)
}

func TestGetChassisCard1(t *testing.T) {
	h, _ := newTestHandler(t, testDPUIdentity)

	chassis := h.GetChassis("Card1")
	assert.Equal(t, "BlueField-3 SuperNIC", *chassis.Model)
	assert.Equal(t, "900-9D3B6-00CV-AA9", *chassis.PartNumber)
	assert.Equal(t, "MT2345ABCD", *chassis.SerialNumber)
	assert.Equal(t, "Nvidia", *chassis.Manufacturer)
}

func TestGetChassisHostPCIeRef(t *testing.T) {
	h, _ := newTestHandler(t, testHostIdentity)
	assert.Equal(t, "/redfish/v1/Chassis/1/PCIeDevices", h.GetChassis("1").PCIeDevices.OdataId)
	assert.Equal(t, "Acme", *h.GetChassis("1").Manufacturer)

	noDPU := testHostIdentity
	noDPU.PCIeDeviceSerial = ""
	h, _ = newTestHandler(t, noDPU)
	assert.Empty(t, h.GetChassis("1").PCIeDevices.OdataId)
}

func TestGetPCIeDevice(t *testing.T) {
	h, _ := newTestHandler(t, testHostIdentity)

	// The device carries the DPU's serial and a populated Status (clients drop a
	// PCIe device with no Status).
	device, err := h.GetPCIeDevice("1", "DPU")
	require.NoError(t, err)
	assert.Equal(t, "MT2345ABCD", *device.SerialNumber)
	assert.Equal(t, "900-9D3B6-00CV-AA0", *device.PartNumber)
	assert.Equal(t, "NVIDIA", *device.Manufacturer)
	assert.Equal(t, server.RESOURCESTATE_ENABLED, *device.Status.State)

	_, err = h.GetPCIeDevice("1", "OTHER")
	assert.Error(t, err)

	noDPU := testHostIdentity
	noDPU.PCIeDeviceSerial = ""
	h, _ = newTestHandler(t, noDPU)
	_, err = h.GetPCIeDevice("1", "DPU")
	assert.Error(t, err)
}

func TestHandleSystemOEM(t *testing.T) {
	h, _ := newTestHandler(t, testDPUIdentity)

	rec := httptest.NewRecorder()
	h.HandleSystemOEM(rec, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/Bluefield/Oem/Nvidia", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "DpuMode", body["Mode"])
	assert.Equal(t, "020000000001", body["BaseMAC"])
	assert.Equal(t, "Disabled", body["HostRshim"])

	h, _ = newTestHandler(t, testHostIdentity)
	rec = httptest.NewRecorder()
	h.HandleSystemOEM(rec, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/1/Oem/Nvidia", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBFBNoOpTaskCompleted(t *testing.T) {
	h, _ := newTestHandler(t, testDPUIdentity)

	update := h.SimpleUpdate()
	assert.Equal(t, server.TASKV173TASKSTATE_COMPLETED, update.TaskState)
	assert.NotEmpty(t, update.Id)

	task := h.GetTask(update.Id)
	assert.Equal(t, server.TASKV173TASKSTATE_COMPLETED, task.TaskState)
	assert.Equal(t, update.Id, task.Id)
}

func TestFirmwareInventory(t *testing.T) {
	h, _ := newTestHandler(t, testDPUIdentity)

	collection := h.GetFirmwareInventoryCollection()
	assert.Equal(t, int64(6), collection.MembersodataCount)

	// Every member serves the version its own annotation configured.
	versions := map[string]string{
		"BMC_Firmware":      "BF-25.10-21",
		"Bluefield_FW_ERoT": "00.02.0195.0000_n03",
		"DPU_NIC":           "32.47.2683",
		"DPU_UEFI":          "4.13.2-12-gabcdef",
		"DPU_SYS_IMAGE":     "0200:0000:0000:0001",
		"DPU_BSP":           "4.5.0.12985",
	}
	for i, member := range collection.Members {
		id := member.OdataId[strings.LastIndex(member.OdataId, "/")+1:]
		inv, err := h.GetFirmwareInventory(id)
		require.NoError(t, err, "member %d", i)
		assert.Equal(t, versions[id], *inv.Version, id)
	}

	_, err := h.GetFirmwareInventory("Nonexistent")
	assert.Error(t, err)

	// A non-DPU has an empty inventory; Members must serialize as [] not null or
	// the Redfish client fails the whole host exploration.
	h, _ = newTestHandler(t, testHostIdentity)
	hostCollection := h.GetFirmwareInventoryCollection()
	assert.Equal(t, int64(0), hostCollection.MembersodataCount)
	assert.Contains(t, string(sanitize(hostCollection)), `"Members":[]`)
	_, err = h.GetFirmwareInventory("BMC_Firmware")
	assert.Error(t, err)
}

func TestGetSecureBoot(t *testing.T) {
	// Both fields must be present: nico treats a response missing either as the
	// BlueField "incomplete SecureBoot" race and force-restarts up to ten times.
	h, _ := newTestHandler(t, testDPUIdentity)
	sb, err := h.GetSecureBoot()
	require.NoError(t, err)
	body := string(sanitize(sb))
	assert.Contains(t, body, `"SecureBootEnable":false`)
	assert.Contains(t, body, `"SecureBootCurrentBoot":"Disabled"`)

	enabled := testDPUIdentity
	enabled.SecureBoot = true
	h, _ = newTestHandler(t, enabled)
	sb, err = h.GetSecureBoot()
	require.NoError(t, err)
	body = string(sanitize(sb))
	assert.Contains(t, body, `"SecureBootEnable":true`)
	assert.Contains(t, body, `"SecureBootCurrentBoot":"Enabled"`)

	h, _ = newTestHandler(t, testHostIdentity)
	_, err = h.GetSecureBoot()
	assert.Error(t, err)
}

func TestGetBios(t *testing.T) {
	h, _ := newTestHandler(t, testDPUIdentity)
	bios, err := h.GetBios()
	require.NoError(t, err)
	assert.Equal(t, "Restricted", bios.Attributes["HostPrivilegeLevel"])
	assert.Equal(t, "Embedded", bios.Attributes["InternalCPUModel"])
	assert.Equal(t, "DpuMode", bios.Attributes["NicMode"])

	h, _ = newTestHandler(t, testHostIdentity)
	hostBios, err := h.GetBios()
	require.NoError(t, err)
	assert.Equal(t, "Enabled", hostBios.Attributes["UsbBoot"])
	assert.Equal(t, "Enabled", hostBios.Attributes["ProcVirtualization"])
	assert.Equal(t, "Enabled", hostBios.Attributes["Dhcpv4"])
	assert.Equal(t, "Auto", hostBios.Attributes["HttpSupport"])
	assert.Equal(t, "/redfish/v1/Systems/1/Bios", hostBios.OdataId)
}

func TestHandleDPUConfigNoop(t *testing.T) {
	h, _ := newTestHandler(t, testDPUIdentity)
	rec := httptest.NewRecorder()
	h.HandleDPUConfigNoop(rec, httptest.NewRequest(http.MethodPatch, "/redfish/v1/Systems/Bluefield/Bios/Settings", nil))
	assert.Equal(t, http.StatusNoContent, rec.Code)

	h, _ = newTestHandler(t, testHostIdentity)
	rec = httptest.NewRecorder()
	h.HandleDPUConfigNoop(rec, httptest.NewRequest(http.MethodPatch, "/redfish/v1/Systems/1/Bios/Settings", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleSystemSettings(t *testing.T) {
	patch := func(h *handler, body string) int {
		rec := httptest.NewRecorder()
		h.HandleSystemSettings(rec, httptest.NewRequest(http.MethodPatch, "/redfish/v1/Systems/Bluefield/Settings", strings.NewReader(body)))
		return rec.Code
	}

	h, mockRM := newTestHandler(t, testDPUIdentity)

	// libredfish's boot-once UefiHttp lands as a one-time boot.
	mockRM.EXPECT().SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP).Return(nil)
	assert.Equal(t, http.StatusNoContent, patch(h, `{"Boot":{"BootSourceOverrideTarget":"UefiHttp","BootSourceOverrideEnabled":"Once"}}`))

	// A body without an override is acknowledged and changes nothing.
	assert.Equal(t, http.StatusNoContent, patch(h, `{}`))

	assert.Equal(t, http.StatusBadRequest, patch(h, `not json`))

	mockRM.EXPECT().SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_PXE).Return(assert.AnError)
	assert.Equal(t, http.StatusBadRequest, patch(h, `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`))

	h, _ = newTestHandler(t, testHostIdentity)
	assert.Equal(t, http.StatusNotFound, patch(h, `{"Boot":{"BootSourceOverrideEnabled":"Once"}}`))
}

func TestHandleDPUActions(t *testing.T) {
	post := func(handle http.HandlerFunc, body string) int {
		rec := httptest.NewRecorder()
		handle(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		return rec.Code
	}

	h, mockRM := newTestHandler(t, testDPUIdentity)

	mockRM.EXPECT().SetHostRshim(true).Return()
	assert.Equal(t, http.StatusNoContent, post(h.HandleHostRshimSet, `{"HostRshim":"Enabled"}`))
	mockRM.EXPECT().SetHostRshim(false).Return()
	assert.Equal(t, http.StatusNoContent, post(h.HandleHostRshimSet, `{"HostRshim":"Disabled"}`))
	assert.Equal(t, http.StatusBadRequest, post(h.HandleHostRshimSet, `{"HostRshim":"Maybe"}`))

	mockRM.EXPECT().SetMode("NicMode").Return()
	assert.Equal(t, http.StatusNoContent, post(h.HandleModeSet, `{"Mode":"NicMode"}`))
	assert.Equal(t, http.StatusBadRequest, post(h.HandleModeSet, `{"Mode":"Weird"}`))

	mockRM.EXPECT().SetSecureBoot(true).Return()
	assert.Equal(t, http.StatusNoContent, post(h.HandleSecureBootPatch, `{"SecureBootEnable":true}`))

	assert.Equal(t, http.StatusAccepted, post(h.HandleSecureBootCertificateAdd, `{}`))

	// None of them exist on a host.
	h, _ = newTestHandler(t, testHostIdentity)
	assert.Equal(t, http.StatusNotFound, post(h.HandleHostRshimSet, `{"HostRshim":"Enabled"}`))
	assert.Equal(t, http.StatusNotFound, post(h.HandleModeSet, `{"Mode":"NicMode"}`))
	assert.Equal(t, http.StatusNotFound, post(h.HandleSecureBootPatch, `{"SecureBootEnable":true}`))
	assert.Equal(t, http.StatusNotFound, post(h.HandleSecureBootCertificateAdd, `{}`))
}

func TestHandleAccountPatch(t *testing.T) {
	patch := func(h *handler, id, body string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/redfish/v1/AccountService/Accounts/"+id, strings.NewReader(body))
		h.HandleAccountPatch(rec, mux.SetURLVars(req, map[string]string{"AccountId": id}))
		return rec.Code
	}

	h, mockRM := newTestHandler(t, testDPUIdentity)

	// Only a hash is persisted, never the password itself.
	var stored string
	mockRM.EXPECT().SetPasswordHash(gomock.Any()).DoAndReturn(func(hash string) error {
		stored = hash
		return nil
	})
	assert.Equal(t, http.StatusNoContent, patch(h, testUsername, `{"Password":"new-secret"}`))
	assert.NotContains(t, stored, "new-secret")
	assert.True(t, session.VerifyPassword(stored, "new-secret"))

	assert.Equal(t, http.StatusBadRequest, patch(h, testUsername, `{"Password":""}`))
	assert.Equal(t, http.StatusBadRequest, patch(h, testUsername, `{"UserName":"x"}`))
	assert.Equal(t, http.StatusNotFound, patch(h, "someone-else", `{"Password":"x"}`))

	mockRM.EXPECT().SetPasswordHash(gomock.Any()).Return(assert.AnError)
	assert.Equal(t, http.StatusInternalServerError, patch(h, testUsername, `{"Password":"new-secret"}`))
}

// TestEmptyCollectionsSerializeAsArray locks in that empty Members collections
// emit "Members":[] (not null) after sanitize. The Redfish client deserializes
// Members into a Vec and rejects null, which previously failed host exploration.
func TestEmptyCollectionsSerializeAsArray(t *testing.T) {
	noDPU := resourcemanager.Identity{SystemID: "1", ManagerID: testHostManagerID}
	h, _ := newTestHandler(t, noDPU)

	fwBody := sanitize(h.GetFirmwareInventoryCollection())
	assert.Contains(t, string(fwBody), `"Members":[]`)
	assert.NotContains(t, string(fwBody), `"Members":null`)

	pcieBody := sanitize(h.GetPCIeDeviceCollection("1"))
	assert.Contains(t, string(pcieBody), `"Members":[]`)
	assert.NotContains(t, string(pcieBody), `"Members":null`)
}
