package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
)

func TestServiceRootVendor(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// A DPU must report NVIDIA so libredfish selects the BlueField client that
	// reads NIC mode and base MAC from the OEM resource. A host reports HPE.
	mockRM.EXPECT().IsDPU().Return(true)
	assert.Equal(t, "NVIDIA", *h.GetServiceRoot().Vendor)

	mockRM.EXPECT().IsDPU().Return(false)
	assert.Equal(t, "HPE", *h.GetServiceRoot().Vendor)
}

func TestGetChassisCollectionDPU(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// A DPU lists the Card1 chassis alongside the default one.
	mockRM.EXPECT().IsDPU().Return(true)
	dpu := h.GetChassisCollection()
	assert.Equal(t, int64(2), dpu.MembersodataCount)
	assert.Equal(t, "/redfish/v1/Chassis/Card1", dpu.Members[1].OdataId)

	// A non-DPU lists only the default chassis.
	mockRM.EXPECT().IsDPU().Return(false)
	host := h.GetChassisCollection()
	assert.Equal(t, int64(1), host.MembersodataCount)
}

func TestGetComputerSystemCollectionDPU(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// A DPU lists its member as "Bluefield"; a non-DPU keeps "1".
	mockRM.EXPECT().IsDPU().Return(true)
	dpu := h.GetComputerSystemCollection()
	assert.Equal(t, "/redfish/v1/Systems/Bluefield", dpu.Members[0].OdataId)

	mockRM.EXPECT().IsDPU().Return(false)
	host := h.GetComputerSystemCollection()
	assert.Equal(t, "/redfish/v1/Systems/1", host.Members[0].OdataId)
}

func TestGetChassisCard1Model(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// Annotation value passes through verbatim.
	mockRM.EXPECT().IsDPU().Return(true)
	mockRM.EXPECT().DPUCard().Return("BlueField 3 DPU")
	chassis := h.GetChassis("Card1")
	assert.Equal(t, "Card1", chassis.Id)
	assert.NotNil(t, chassis.Model)
	assert.Equal(t, "BlueField 3 DPU", *chassis.Model)

	// Absent annotation falls back to the default model.
	mockRM.EXPECT().IsDPU().Return(true)
	mockRM.EXPECT().DPUCard().Return("")
	chassis = h.GetChassis("Card1")
	assert.Equal(t, "BlueField-3 DPU", *chassis.Model)
}

func TestGetChassisHostPCIeRef(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// Host chassis points at its PCIe inventory only when a DPU is attached.
	mockRM.EXPECT().IsDPU().Return(false)
	mockRM.EXPECT().AttachedDPUSerial().Return("DPU-BM1", true)
	withDPU := h.GetChassis("1")
	assert.Equal(t, "/redfish/v1/Chassis/1/PCIeDevices", withDPU.PCIeDevices.OdataId)

	mockRM.EXPECT().IsDPU().Return(false)
	mockRM.EXPECT().AttachedDPUSerial().Return("", false)
	withoutDPU := h.GetChassis("1")
	assert.Empty(t, withoutDPU.PCIeDevices.OdataId)
}

func TestGetPCIeDevice(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// The attached DPU device carries the matching serial and a populated Status
	// (clients drop a PCIe device with no Status).
	mockRM.EXPECT().AttachedDPUSerial().Return("DPU-BM1", true)
	device, err := h.GetPCIeDevice("1", "DPU")
	assert.NoError(t, err)
	assert.NotNil(t, device.SerialNumber)
	assert.Equal(t, "DPU-BM1", *device.SerialNumber)
	assert.NotNil(t, device.Status.State)
	assert.Equal(t, server.RESOURCESTATE_ENABLED, *device.Status.State)

	// An unknown device id is not found.
	mockRM.EXPECT().AttachedDPUSerial().Return("DPU-BM1", true)
	_, err = h.GetPCIeDevice("1", "OTHER")
	assert.Error(t, err)

	// No attached DPU means no device.
	mockRM.EXPECT().AttachedDPUSerial().Return("", false)
	_, err = h.GetPCIeDevice("1", "DPU")
	assert.Error(t, err)
}

func TestHandleSystemOEM(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// DPU: Mode is reported in libredfish's form and BaseMAC has colons stripped.
	mockRM.EXPECT().IsDPU().Return(true)
	mockRM.EXPECT().SystemOEM().Return("DPU", "02:00:00:00:00:01")

	rec := httptest.NewRecorder()
	h.HandleSystemOEM(rec, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/Bluefield/Oem/Nvidia", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "DpuMode", body["Mode"])
	assert.Equal(t, "020000000001", body["BaseMAC"])

	// Non-DPU: the OEM resource is absent.
	mockRM.EXPECT().IsDPU().Return(false)
	rec = httptest.NewRecorder()
	h.HandleSystemOEM(rec, httptest.NewRequest(http.MethodGet, "/redfish/v1/Systems/1/Oem/Nvidia", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBFBNoOpTaskCompleted(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// SimpleUpdate and any subsequent task lookup report the same completed task.
	update := h.SimpleUpdate()
	assert.Equal(t, server.TASKV173TASKSTATE_COMPLETED, update.TaskState)
	assert.NotEmpty(t, update.Id)

	task := h.GetTask(update.Id)
	assert.Equal(t, server.TASKV173TASKSTATE_COMPLETED, task.TaskState)
	assert.Equal(t, update.Id, task.Id)
}

func TestFirmwareInventory(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	// A DPU lists the BMC_Firmware member; a non-DPU keeps an empty inventory.
	mockRM.EXPECT().IsDPU().Return(true)
	dpuCollection := h.GetFirmwareInventoryCollection()
	assert.Equal(t, int64(1), dpuCollection.MembersodataCount)
	assert.Equal(t, "/redfish/v1/UpdateService/FirmwareInventory/BMC_Firmware", dpuCollection.Members[0].OdataId)

	mockRM.EXPECT().IsDPU().Return(false)
	hostCollection := h.GetFirmwareInventoryCollection()
	assert.Equal(t, int64(0), hostCollection.MembersodataCount)
	// Members must be a non-nil empty slice so it serializes as [] not null;
	// the Redfish client rejects null and fails the whole host exploration.
	assert.NotNil(t, hostCollection.Members)

	// The DPU's BMC_Firmware entry uses the annotation-driven version.
	mockRM.EXPECT().IsDPU().Return(true)
	mockRM.EXPECT().BMCFirmwareVersion().Return("BF-24.10-5", true)
	inv, err := h.GetFirmwareInventory("BMC_Firmware")
	assert.NoError(t, err)
	assert.NotNil(t, inv.Version)
	assert.Equal(t, "BF-24.10-5", *inv.Version)

	// When no annotation is set, default to a BF- >= 24.10 version.
	mockRM.EXPECT().IsDPU().Return(true)
	mockRM.EXPECT().BMCFirmwareVersion().Return("", false)
	inv, err = h.GetFirmwareInventory("BMC_Firmware")
	assert.NoError(t, err)
	assert.Equal(t, "BF-24.10-1", *inv.Version)

	// A non-DPU has no firmware inventory entry.
	mockRM.EXPECT().IsDPU().Return(false)
	_, err = h.GetFirmwareInventory("BMC_Firmware")
	assert.Error(t, err)
}

// TestEmptyCollectionsSerializeAsArray locks in that empty Members collections
// emit "Members":[] (not null) after sanitize. The Redfish client deserializes
// Members into a Vec and rejects null, which previously failed host exploration.
func TestEmptyCollectionsSerializeAsArray(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	h := NewHandler(testUsername, testPassword, mockRM)

	mockRM.EXPECT().IsDPU().Return(false)
	fwBody := sanitize(h.GetFirmwareInventoryCollection())
	assert.Contains(t, string(fwBody), `"Members":[]`)
	assert.NotContains(t, string(fwBody), `"Members":null`)

	mockRM.EXPECT().AttachedDPUSerial().Return("", false)
	pcieBody := sanitize(h.GetPCIeDeviceCollection("1"))
	assert.Contains(t, string(pcieBody), `"Members":[]`)
	assert.NotContains(t, string(pcieBody), `"Members":null`)
}
