package resourcemanager

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"

	"kubevirt.io/kubevirtbmc/pkg/builder"
)

const (
	zeroUUID = "00000000-0000-0000-0000-000000000000"

	testVMAUID = "1b4e28ba-2fa1-11d2-883f-0016d3cca427"
	testVMBUID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

// NICo hashes the product/board/chassis serials into its MachineId and rejects
// anything that does not match this.
var nicoSerialPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)

// asMap round-trips a resource through JSON so assertions run against the bytes
// a Redfish client actually receives. Both UUID fields are tagged `omitempty`
// in the generated models, so an empty value disappears from the payload
// entirely rather than merely being wrong: asserting on the Go struct would not
// catch that.
func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func vmWithUID(name, uid string) *kubevirtv1.VirtualMachine {
	vm := builder.NewVirtualMachineBuilder(testNamespace, name).Ready(true).Build()
	vm.UID = types.UID(uid)
	return vm
}

func initializedManager(t *testing.T, vm *kubevirtv1.VirtualMachine) *VirtualMachineResourceManager {
	t.Helper()
	vmrm := NewVirtualMachineResourceManager(context.TODO(), kubevirtfake.NewSimpleClientset(vm), nil)
	require.NoError(t, vmrm.Initialize(vm.Namespace, vm.Name))
	return vmrm
}

// identity pulls the three fields a client uses to tell one machine from
// another out of the marshalled documents.
func identity(t *testing.T, vmrm *VirtualMachineResourceManager) (serial, systemUUID, managerUUID string) {
	t.Helper()

	cs, err := vmrm.GetComputerSystem()
	require.NoError(t, err)
	csBody := asMap(t, cs.(*ComputerSystemAdapter).ComputerSystem())

	mgr, err := vmrm.GetManager()
	require.NoError(t, err)
	mgrBody := asMap(t, mgr.(*ManagerAdapter).Manager())

	serial, ok := csBody["SerialNumber"].(string)
	require.True(t, ok, "ComputerSystem.SerialNumber must be present, got %T", csBody["SerialNumber"])
	systemUUID, ok = csBody["UUID"].(string)
	require.True(t, ok, "ComputerSystem.UUID must be present, got %T", csBody["UUID"])
	managerUUID, ok = mgrBody["UUID"].(string)
	require.True(t, ok, "Manager.UUID must be present, got %T", mgrBody["UUID"])

	return serial, systemUUID, managerUUID
}

// Both UUIDs were hardcoded to the all-zeros UUID, so every machine in a fleet
// reported the same identity and nothing keying on the system or BMC UUID could
// tell two hosts apart. They must come from the VM UID, like the serial does.
func TestInitialize_IdentityDerivedFromVMUID(t *testing.T) {
	vmrm := initializedManager(t, vmWithUID(testVMName, testVMAUID))

	serial, systemUUID, managerUUID := identity(t, vmrm)

	assert.Equal(t, strings.ReplaceAll(testVMAUID, "-", ""), serial,
		"serial must be the VM UID with dashes stripped")
	assert.Regexp(t, nicoSerialPattern, serial)

	assert.Equal(t, testVMAUID, systemUUID, "the ComputerSystem is the VM, so it reports the VM UID")

	assert.NotEqual(t, zeroUUID, systemUUID)
	assert.NotEqual(t, zeroUUID, managerUUID)

	_, err := uuid.Parse(managerUUID)
	assert.NoError(t, err, "Manager.UUID must be a canonical UUID, the schema constrains it")
	assert.NotEqual(t, systemUUID, managerUUID,
		"the Manager is a distinct resource from the ComputerSystem")
}

// Two VMs must not be confusable. This is the failure the all-zeros UUIDs
// caused in the field: three machines, one identity.
func TestInitialize_IdentityDistinctPerVM(t *testing.T) {
	serialA, systemA, managerA := identity(t, initializedManager(t, vmWithUID("vm-a", testVMAUID)))
	serialB, systemB, managerB := identity(t, initializedManager(t, vmWithUID("vm-b", testVMBUID)))

	assert.NotEqual(t, serialA, serialB)
	assert.NotEqual(t, systemA, systemB)
	assert.NotEqual(t, managerA, managerB)
}

// The agent rebuilds its resources on every start, so the identity has to be a
// pure function of the VM: a client that saw a machine before a pod restart
// must still recognise it afterwards.
func TestInitialize_IdentityStableAcrossRestart(t *testing.T) {
	vm := vmWithUID(testVMName, testVMAUID)

	serial1, system1, manager1 := identity(t, initializedManager(t, vm))
	serial2, system2, manager2 := identity(t, initializedManager(t, vm))

	assert.Equal(t, serial1, serial2)
	assert.Equal(t, system1, system2)
	assert.Equal(t, manager1, manager2)
}

// A VM whose UID is missing or not a UUID must still get valid, distinct UUIDs.
// Returning "" here would be worse than the bug being fixed: `omitempty` drops
// the field, so the client sees no UUID at all.
func TestResourceUUIDs_FallsBackWhenUIDUnparseable(t *testing.T) {
	for _, uid := range []string{"", "not-a-uuid"} {
		t.Run("uid="+uid, func(t *testing.T) {
			systemA, managerA := resourceUUIDs(vmWithUID("vm-a", uid))
			systemB, managerB := resourceUUIDs(vmWithUID("vm-b", uid))

			for _, got := range []string{systemA, managerA, systemB, managerB} {
				require.NotEmpty(t, got, "an empty UUID is dropped by omitempty")
				_, err := uuid.Parse(got)
				require.NoError(t, err)
				require.NotEqual(t, zeroUUID, got)
			}

			assert.NotEqual(t, systemA, managerA)
			assert.NotEqual(t, systemA, systemB, "fallback must still be unique per VM")
			assert.NotEqual(t, managerA, managerB)
		})
	}
}
