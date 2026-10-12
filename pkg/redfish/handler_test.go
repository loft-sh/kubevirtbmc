package redfish

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
	"kubevirt.io/kubevirtbmc/pkg/resourcemanager"
	"kubevirt.io/kubevirtbmc/pkg/session"
)

const (
	testUsername = "admin"
	testPassword = "admin123"
)

// The identities carry values that differ from the defaults, so a test passing
// proves the handlers serve what the VM annotations configured.
var (
	testDPUIdentity = resourcemanager.Identity{
		DPU:                    true,
		Vendor:                 "Nvidia",
		Product:                "BlueField-3 DPU",
		Manufacturer:           "Nvidia",
		Model:                  "BlueField-3 DPU",
		SystemID:               "Bluefield",
		ManagerID:              "Bluefield_BMC",
		SystemSerial:           "MT2345ABCD",
		DPUModel:               "BlueField-3 SuperNIC",
		PartNumber:             "900-9D3B6-00CV-AA9",
		Mode:                   "DpuMode",
		BaseMAC:                "02:00:00:00:00:01",
		HostRshim:              "Disabled",
		BMCFirmware:            "BF-25.10-21",
		CECFirmware:            "00.02.0195.0000_n03",
		NICFirmware:            "32.47.2683",
		UEFIFirmware:           "4.13.2-12-gabcdef",
		SysImageFirmware:       "0200:0000:0000:0001",
		BSPFirmware:            "4.5.0.12985",
		PCIeDevicePartNumber:   "900-9D3B6-00CV-AA0",
		PCIeDeviceManufacturer: "NVIDIA",
		OOBInterface:           "oob_net0",
	}

	testHostIdentity = resourcemanager.Identity{
		Vendor:                 "HPE",
		Manufacturer:           "Acme",
		Model:                  "Acme 1000",
		SystemID:               "1",
		ManagerID:              testHostManagerID,
		SystemSerial:           "HOSTSERIAL1",
		PCIeDeviceSerial:       "MT2345ABCD",
		PCIeDevicePartNumber:   "900-9D3B6-00CV-AA0",
		PCIeDeviceManufacturer: "NVIDIA",
		HostBootMAC:            testBootMAC,
	}
)

// newTestHandler returns a handler whose resource manager reports id. Tests add
// expectations for the calls they exercise on the returned mock.
func newTestHandler(t *testing.T, id resourcemanager.Identity) (*handler, *resourcemanager.MockResourceManager) {
	t.Helper()

	mockRM := resourcemanager.NewMockResourceManager(gomock.NewController(t))
	mockRM.EXPECT().Identity().Return(id).AnyTimes()
	mockRM.EXPECT().PasswordHash().Return("").AnyTimes()

	return NewHandler(testUsername, testPassword, mockRM), mockRM
}

func TestAuthenticate(t *testing.T) {
	h, _ := newTestHandler(t, testHostIdentity)

	testCases := []struct {
		username    string
		password    string
		expectError bool
	}{
		{username: "", password: "", expectError: true},
		{username: "invalid", password: "credentials", expectError: true},
		{username: "admin", password: "admin123", expectError: false},
	}

	for _, tc := range testCases {
		_, _, err := h.Authenticate(&tc.username, &tc.password)
		if tc.expectError {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
		}
	}
}

func TestGetSession(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	h := NewHandler(testUsername, testPassword, nil)

	testCases := []struct {
		name          string
		setupFunc     func()
		sessionID     string
		username      string
		expectedError bool
	}{
		{
			name: "valid session",
			setupFunc: func() {
				tokenInfo := session.NewTokenInfo("test-session-id", "admin")
				session.AddToken(tokenInfo)
			},
			sessionID:     "test-session-id",
			username:      testUsername,
			expectedError: false,
		},
		{
			name:          "invalid session",
			setupFunc:     func() {},
			sessionID:     "invalid-session-id",
			username:      "",
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setupFunc != nil {
				tc.setupFunc()
			}

			id, username, err := h.GetSession(tc.sessionID)
			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.sessionID, id)
				assert.Equal(t, tc.username, username)
			}
		})
	}
}

func TestDeleteSession(t *testing.T) {
	ctl := gomock.NewController(t)
	defer ctl.Finish()

	h := NewHandler(testUsername, testPassword, nil)

	testCases := []struct {
		name      string
		setupFunc func()
		sessionID string
	}{
		{
			name: "valid session",
			setupFunc: func() {
				tokenInfo := session.NewTokenInfo("test-session-id", testUsername)
				session.AddToken(tokenInfo)
			},
			sessionID: "test-session-id",
		},
		{
			name:      "non-existent session",
			setupFunc: func() {},
			sessionID: "invalid-session-id",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setupFunc != nil {
				tc.setupFunc()
			}

			h.DeleteSession(tc.sessionID)
			_, exists := session.GetToken(tc.sessionID)
			assert.False(t, exists)
		})
	}
}

func TestPatchComputerSystem(t *testing.T) {
	once := server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE
	continuous := server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_CONTINUOUS
	disabled := server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED

	testCases := []struct {
		name        string
		enabled     server.ComputerSystemV1220BootSourceOverrideEnabled
		target      server.ComputerSystemBootSource
		expect      func(rm *resourcemanager.MockResourceManager)
		expectError bool
	}{
		{
			name:    "once arms a one-time boot",
			enabled: once,
			target:  server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP,
			expect: func(rm *resourcemanager.MockResourceManager) {
				rm.EXPECT().SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP).Return(nil)
			},
		},
		{
			name:    "disabled cancels a pending one-time boot",
			enabled: disabled,
			expect: func(rm *resourcemanager.MockResourceManager) {
				rm.EXPECT().SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_NONE).Return(nil)
			},
		},
		{
			name:    "continuous PXE is persistent",
			enabled: continuous,
			target:  server.COMPUTERSYSTEMBOOTSOURCE_PXE,
			expect: func(rm *resourcemanager.MockResourceManager) {
				rm.EXPECT().SetBootDevice(resourcemanager.BootDevicePxe).Return(nil)
			},
		},
		{
			name:    "continuous HDD is persistent",
			enabled: continuous,
			target:  server.COMPUTERSYSTEMBOOTSOURCE_HDD,
			expect: func(rm *resourcemanager.MockResourceManager) {
				rm.EXPECT().SetBootDevice(resourcemanager.BootDeviceHdd).Return(nil)
			},
		},
		{
			name:    "continuous CD is persistent",
			enabled: continuous,
			target:  server.COMPUTERSYSTEMBOOTSOURCE_CD,
			expect: func(rm *resourcemanager.MockResourceManager) {
				rm.EXPECT().SetBootDevice(resourcemanager.BootDeviceCd).Return(nil)
			},
		},
		{
			name:    "continuous unknown target is ignored",
			enabled: continuous,
			target:  "INVALID_TARGET",
			expect:  func(rm *resourcemanager.MockResourceManager) {},
		},
		{
			name:    "once rejected by the resource manager",
			enabled: once,
			target:  server.COMPUTERSYSTEMBOOTSOURCE_PXE,
			expect: func(rm *resourcemanager.MockResourceManager) {
				rm.EXPECT().SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_PXE).Return(assert.AnError)
			},
			expectError: true,
		},
		{
			name:    "persistent device failure is returned",
			enabled: continuous,
			target:  server.COMPUTERSYSTEMBOOTSOURCE_HDD,
			expect: func(rm *resourcemanager.MockResourceManager) {
				rm.EXPECT().SetBootDevice(resourcemanager.BootDeviceHdd).Return(assert.AnError)
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			h, mockRM := newTestHandler(t, testHostIdentity)
			tc.expect(mockRM)

			err := h.PatchComputerSystem(&server.ComputerSystemV1220ComputerSystem{
				Boot: server.ComputerSystemV1220Boot{
					BootSourceOverrideEnabled: tc.enabled,
					BootSourceOverrideTarget:  tc.target,
				},
			})

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVerifyCredentials(t *testing.T) {
	hash, err := session.HashPassword("rotated")
	assert.NoError(t, err)

	mockRM := resourcemanager.NewMockResourceManager(gomock.NewController(t))
	mockRM.EXPECT().PasswordHash().Return(hash).AnyTimes()
	h := NewHandler(testUsername, testPassword, mockRM)

	// Once the password was rotated, the startup password stops working.
	assert.True(t, h.VerifyCredentials(testUsername, "rotated"))
	assert.False(t, h.VerifyCredentials(testUsername, testPassword))
	assert.False(t, h.VerifyCredentials("other", "rotated"))
}

func TestComputerSystemReset(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRM := resourcemanager.NewMockResourceManager(ctrl)
	handler := NewHandler(testUsername, testPassword, mockRM)

	testCases := []struct {
		name          string
		resetType     server.ResourceResetType
		mockSetup     func()
		expectedError bool
	}{
		{
			name:      "power on reset",
			resetType: server.RESOURCERESETTYPE_ON,
			mockSetup: func() {
				mockRM.EXPECT().PowerOn().Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "graceful shutdown reset",
			resetType: server.RESOURCERESETTYPE_GRACEFUL_SHUTDOWN,
			mockSetup: func() {
				mockRM.EXPECT().PowerOff().Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "graceful restart reset",
			resetType: server.RESOURCERESETTYPE_GRACEFUL_RESTART,
			mockSetup: func() {
				mockRM.EXPECT().PowerCycle().Return(nil)
			},
			expectedError: false,
		},
		{
			name:      "force restart reset",
			resetType: server.RESOURCERESETTYPE_FORCE_RESTART,
			mockSetup: func() {
				mockRM.EXPECT().PowerCycle().Return(nil)
			},
			expectedError: false,
		},
		{
			name:          "unsupported reset type",
			resetType:     server.ResourceResetType("Unsupported"),
			mockSetup:     func() {}, // No expectations for unsupported reset types
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.mockSetup()
			err := handler.ComputerSystemReset(tc.resetType)
			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
