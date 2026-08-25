package resourcemanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"

	"kubevirt.io/kubevirtbmc/pkg/builder"
)

// NewManager runs exactly once, from Initialize. Before this fix GetManager was
// a bare accessor, so the DateTime baked in at Initialize was the only clock
// the BMC ever reported: the drift a client measures against it grew by one
// second per second of pod uptime. Clients that gate on BMC clock drift (NICo
// uses a 300s threshold, and then enters a destructive remediation loop) would
// fail every BMC that had been up for more than five minutes.
func TestGetManager_RefreshesDateTime(t *testing.T) {
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build()

	m := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: kubevirtfake.NewSimpleClientset(vm),
		namespace:  testNamespace,
		name:       testVMName,
	}
	require.NoError(t, m.Initialize(testNamespace, testVMName))

	// Rewind the clock the adapter is holding to simulate a pod that started
	// an hour ago, which is what Initialize would have left behind.
	stale := time.Now().Add(-time.Hour).UTC()
	m.manager.SetDateTime(stale)
	require.Equal(t, formatBMCDateTime(stale), *m.manager.(*ManagerAdapter).Manager().DateTime)

	before := time.Now().UTC()
	got, err := m.GetManager()
	require.NoError(t, err)
	after := time.Now().UTC()

	adapter, ok := got.(*ManagerAdapter)
	require.True(t, ok)
	rawReported := adapter.Manager().DateTime
	require.NotNil(t, rawReported)
	reported, err := parseBMCDateTime(*rawReported)
	require.NoError(t, err, "DateTime must be servable back as a timestamp")

	assert.False(t, reported.Before(before.Truncate(time.Second)), "DateTime must be refreshed on read, got stale %s", reported)
	assert.False(t, reported.After(after), "DateTime must not be in the future, got %s", reported)

	// The whole point: drift measured against a client clock stays ~zero
	// regardless of how long the agent has been running.
	assert.Less(t, time.Since(reported).Abs(), 5*time.Second)
}

// Two reads separated in time must report two different clocks. A single
// refresh at first read would still freeze afterwards.
//
// The sleep is just over a second because a BMC reports whole seconds, so two
// reads inside the same second legitimately report the same value.
func TestGetManager_DateTimeAdvancesBetweenReads(t *testing.T) {
	vm := builder.NewVirtualMachineBuilder(testNamespace, testVMName).Ready(true).Build()

	m := &VirtualMachineResourceManager{
		ctx:        context.TODO(),
		virtClient: kubevirtfake.NewSimpleClientset(vm),
		namespace:  testNamespace,
		name:       testVMName,
	}
	require.NoError(t, m.Initialize(testNamespace, testVMName))

	first, err := m.GetManager()
	require.NoError(t, err)
	t1 := mustParseBMCDateTime(t, *first.(*ManagerAdapter).Manager().DateTime)

	time.Sleep(1100 * time.Millisecond)

	second, err := m.GetManager()
	require.NoError(t, err)
	t2 := mustParseBMCDateTime(t, *second.(*ManagerAdapter).Manager().DateTime)

	assert.True(t, t2.After(t1), "second read (%s) must be later than first (%s)", t2, t1)
}

func TestGetManager_UninitializedErrors(t *testing.T) {
	m := &VirtualMachineResourceManager{ctx: context.TODO()}
	_, err := m.GetManager()
	assert.Error(t, err)
}

// The reported clock must be UTC with a matching offset, otherwise a client
// comparing timestamps reads the offset as drift.
func TestNewManager_ReportsUTC(t *testing.T) {
	adapter := NewManager("BMC", "Manager", "00000000-0000-0000-0000-000000000000")
	mgr := adapter.Manager()

	require.NotNil(t, mgr.DateTime)
	// Reported as a BMC reports it: a literal +00:00 offset and whole seconds,
	// not RFC3339Nano's `Z` and fractional digits. The value is parsed by
	// clients to measure BMC clock drift, so the format is load-bearing.
	assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\+00:00$`, *mgr.DateTime)
	parsed, err := parseBMCDateTime(*mgr.DateTime)
	require.NoError(t, err)
	_, offset := parsed.Zone()
	assert.Zero(t, offset, "the reported clock must be UTC")
	require.NotNil(t, mgr.DateTimeLocalOffset)
	assert.Equal(t, "+00:00", *mgr.DateTimeLocalOffset)
}

func parseBMCDateTime(value string) (time.Time, error) {
	return time.Parse(bmcDateTimeLayout, value)
}

func mustParseBMCDateTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := parseBMCDateTime(value)
	require.NoError(t, err)
	return parsed
}
