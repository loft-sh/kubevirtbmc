package resourcemanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"

	"kubevirt.io/kubevirtbmc/pkg/generated/redfish/server"
)

// newBootOnceFixture returns an initialized manager for a running DPU VM whose
// interfaces are [pf, oob_net0] and whose only disk is rootdisk, with no boot
// order set anywhere.
func newBootOnceFixture(t *testing.T, interfaces ...string) (*VirtualMachineResourceManager, *kubevirtfake.Clientset) {
	t.Helper()

	if len(interfaces) == 0 {
		interfaces = []string{"pf", "oob_net0"}
	}

	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   testNamespace,
			Name:        testVMName,
			UID:         "vm-uid",
			Annotations: map[string]string{annotationDPU: annotationTrue},
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{},
		},
		Status: kubevirtv1.VirtualMachineStatus{Ready: true},
	}
	devices := &vm.Spec.Template.Spec.Domain.Devices
	devices.Disks = []kubevirtv1.Disk{{Name: testRootDisk}}
	for _, name := range interfaces {
		devices.Interfaces = append(devices.Interfaces, kubevirtv1.Interface{Name: name, MacAddress: "02:00:00:00:00:10"})
	}

	vmi := &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testVMName, UID: "vmi-1"},
	}

	client := kubevirtfake.NewSimpleClientset(vm, vmi)
	m := NewVirtualMachineResourceManager(context.Background(), client, nil)
	m.bootRestoreInterval = 5 * time.Millisecond
	m.bootRestoreTimeout = 5 * time.Second
	require.NoError(t, m.Initialize(testNamespace, testVMName))

	return m, client
}

// bootOrdersOf returns the boot order of every interface and disk of the VM,
// 0 meaning unset.
func bootOrdersOf(t *testing.T, client *kubevirtfake.Clientset) map[string]uint {
	t.Helper()

	vm, err := client.KubevirtV1().VirtualMachines(testNamespace).Get(context.Background(), testVMName, metav1.GetOptions{})
	require.NoError(t, err)

	orders := map[string]uint{}
	for _, iface := range vm.Spec.Template.Spec.Domain.Devices.Interfaces {
		orders[iface.Name] = 0
		if iface.BootOrder != nil {
			orders[iface.Name] = *iface.BootOrder
		}
	}
	for _, disk := range vm.Spec.Template.Spec.Domain.Devices.Disks {
		orders[disk.Name] = 0
		if disk.BootOrder != nil {
			orders[disk.Name] = *disk.BootOrder
		}
	}

	return orders
}

// replaceVMI simulates KubeVirt restarting the VM: a VMI with a new UID.
func replaceVMI(t *testing.T, client *kubevirtfake.Clientset, uid types.UID) {
	t.Helper()

	vmis := client.KubevirtV1().VirtualMachineInstances(testNamespace)
	require.NoError(t, vmis.Delete(context.Background(), testVMName, metav1.DeleteOptions{}))
	_, err := vmis.Create(context.Background(), &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: testVMName, UID: uid},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

func TestBootOnce_NetworkBootAppliesOnceAndReverts(t *testing.T) {
	m, client := newBootOnceFixture(t)
	original := bootOrdersOf(t, client)

	require.NoError(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP))

	// Arming alone leaves the VM untouched and is visible as a pending override.
	require.Equal(t, original, bootOrdersOf(t, client))
	cs := m.computerSystem.(*ComputerSystemAdapter).ComputerSystem()
	require.Equal(t, server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_ONCE, cs.Boot.BootSourceOverrideEnabled)
	require.Equal(t, server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP, cs.Boot.BootSourceOverrideTarget)

	// The restart boots the OOB interface first and falls back to the disk, and
	// the override is consumed.
	require.NoError(t, m.PowerCycle())
	require.Equal(t, map[string]uint{"pf": 0, "oob_net0": 1, testRootDisk: 2}, bootOrdersOf(t, client))
	require.Equal(t, server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED, cs.Boot.BootSourceOverrideEnabled)

	// Once KubeVirt has started the new VMI, which copied the boot order, the
	// original order is back for every later boot.
	replaceVMI(t, client, "vmi-2")
	require.Eventually(t, func() bool {
		return bootOrdersOf(t, client)["oob_net0"] == 0
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, original, bootOrdersOf(t, client))

	// A second restart is a plain restart.
	require.NoError(t, m.PowerCycle())
	require.Equal(t, original, bootOrdersOf(t, client))
}

func TestBootOnce_NamedOOBInterfaceWins(t *testing.T) {
	m, client := newBootOnceFixture(t, "oob_net0", "pf")

	require.NoError(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_PXE))
	require.NoError(t, m.PowerCycle())
	require.Equal(t, uint(1), bootOrdersOf(t, client)["oob_net0"])
	require.Zero(t, bootOrdersOf(t, client)["pf"])
}

func TestBootOnce_FirstInterfaceWhenNoOOBInterface(t *testing.T) {
	m, client := newBootOnceFixture(t, "eth0", "eth1")

	require.NoError(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP))
	require.NoError(t, m.PowerCycle())
	require.Equal(t, map[string]uint{"eth0": 1, "eth1": 0, testRootDisk: 2}, bootOrdersOf(t, client))
}

func TestBootOnce_PowerOnOfRunningVMDoesNotConsume(t *testing.T) {
	m, client := newBootOnceFixture(t)
	original := bootOrdersOf(t, client)

	require.NoError(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP))
	require.NoError(t, m.PowerOn())
	require.Equal(t, original, bootOrdersOf(t, client))

	require.NoError(t, m.PowerCycle())
	require.Equal(t, uint(1), bootOrdersOf(t, client)["oob_net0"])
}

func TestBootOnce_DiskOrNoneCancels(t *testing.T) {
	for _, target := range []server.ComputerSystemBootSource{
		server.COMPUTERSYSTEMBOOTSOURCE_HDD,
		server.COMPUTERSYSTEMBOOTSOURCE_NONE,
	} {
		t.Run(string(target), func(t *testing.T) {
			m, client := newBootOnceFixture(t)
			original := bootOrdersOf(t, client)

			require.NoError(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP))
			require.NoError(t, m.SetBootOnce(target))
			require.NoError(t, m.PowerCycle())

			require.Equal(t, original, bootOrdersOf(t, client))
			cs := m.computerSystem.(*ComputerSystemAdapter).ComputerSystem()
			require.Equal(t, server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED, cs.Boot.BootSourceOverrideEnabled)
		})
	}
}

func TestBootOnce_RejectsWhatCannotBeApplied(t *testing.T) {
	t.Run("no interfaces for a network boot", func(t *testing.T) {
		m, client := newBootOnceFixture(t)
		vm, err := client.KubevirtV1().VirtualMachines(testNamespace).Get(context.Background(), testVMName, metav1.GetOptions{})
		require.NoError(t, err)
		vm.Spec.Template.Spec.Domain.Devices.Interfaces = nil
		_, err = client.KubevirtV1().VirtualMachines(testNamespace).Update(context.Background(), vm, metav1.UpdateOptions{})
		require.NoError(t, err)

		require.Error(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP))
		cs := m.computerSystem.(*ComputerSystemAdapter).ComputerSystem()
		require.Equal(t, server.COMPUTERSYSTEMV1220BOOTSOURCEOVERRIDEENABLED_DISABLED, cs.Boot.BootSourceOverrideEnabled)
	})

	t.Run("unsupported source", func(t *testing.T) {
		m, _ := newBootOnceFixture(t)
		require.Error(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_USB))
	})

	t.Run("cd without a cdrom", func(t *testing.T) {
		m, _ := newBootOnceFixture(t)
		require.Error(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_CD))
	})
}

func TestBootOnce_FailedRestartKeepsOverrideArmed(t *testing.T) {
	m, client := newBootOnceFixture(t)
	original := bootOrdersOf(t, client)

	client.PrependReactor("put", "virtualmachines", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "restart" {
			return false, nil, nil
		}
		return true, nil, context.DeadlineExceeded
	})

	require.NoError(t, m.SetBootOnce(server.COMPUTERSYSTEMBOOTSOURCE_UEFI_HTTP))
	require.Error(t, m.PowerCycle())
	require.Equal(t, original, bootOrdersOf(t, client))

	// The override was not spent, so a retry still boots from the network.
	client.ReactionChain = client.ReactionChain[1:]
	require.NoError(t, m.PowerCycle())
	require.Equal(t, uint(1), bootOrdersOf(t, client)["oob_net0"])
}

func TestPasswordHash_PersistedOnVM(t *testing.T) {
	m, client := newBootOnceFixture(t)
	require.Empty(t, m.PasswordHash())

	require.NoError(t, m.SetPasswordHash("pbkdf2-sha256$1$c$k"))
	require.Equal(t, "pbkdf2-sha256$1$c$k", m.PasswordHash())

	// A new BMC pod for the same VM picks the rotated password up again.
	restarted := NewVirtualMachineResourceManager(context.Background(), client, nil)
	require.NoError(t, restarted.Initialize(testNamespace, testVMName))
	require.Equal(t, "pbkdf2-sha256$1$c$k", restarted.PasswordHash())
}
