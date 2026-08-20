package redfish

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NICo's site-explorer refuses to explore a BMC whose ServiceRoot reports no
// recognized vendor:
//
//	BMC ServiceRoot (/redfish/v1) did not report a recognized vendor
//	(observed vendor/oem = None)
//
// virtbmc reported neither Vendor nor Oem, so there has to be a vendor in the
// payload even with nothing configured.
func TestServiceRoot_ReportsVendorByDefault(t *testing.T) {
	t.Setenv(VendorEnvVar, "")
	t.Setenv(ProductEnvVar, "")

	h := NewHandler(testUsername, testPassword, nil)
	body := encodeAsServed(t, h.GetServiceRoot())

	vendor, ok := body["Vendor"].(string)
	require.True(t, ok, "Vendor must be reported, got %T", body["Vendor"])
	assert.NotEmpty(t, vendor)
	assert.Equal(t, defaultVendor, vendor)
}

// Which vendor identity to claim is a question about the client, not about
// virtbmc, so it is configuration. Rebuilding the image to try another value
// is not an acceptable loop.
func TestServiceRoot_VendorIsConfigurable(t *testing.T) {
	for _, vendor := range []string{"Dell", "HPE", "Lenovo", "NVIDIA", "Supermicro"} {
		t.Run(vendor, func(t *testing.T) {
			t.Setenv(VendorEnvVar, vendor)

			h := NewHandler(testUsername, testPassword, nil)

			assert.Equal(t, vendor, encodeAsServed(t, h.GetServiceRoot())["Vendor"])
		})
	}
}

// These values arrive by way of a Secret or a chart value often enough to pick
// up a trailing newline. A client that matches the vendor string exactly, as
// NICo does after lowercasing, would not recognize "Dell\n".
func TestIdentityFromEnv_TrimsSurroundingWhitespace(t *testing.T) {
	t.Setenv(VendorEnvVar, "  Dell\n")
	t.Setenv(ProductEnvVar, "\tPowerEdge R760 \n")

	identity := identityFromEnv()

	assert.Equal(t, "Dell", identity.vendor)
	assert.Equal(t, "PowerEdge R760", identity.product)
}

// A variable set to nothing but whitespace is indistinguishable from one that
// was never set, and must not leave ServiceRoot reporting a blank vendor.
func TestIdentityFromEnv_BlankVendorFallsBackToDefault(t *testing.T) {
	t.Setenv(VendorEnvVar, "   \n\t ")

	assert.Equal(t, defaultVendor, identityFromEnv().vendor)
}

// Product is a hint, not a gate. Absent is how Redfish says "not reported",
// whereas an empty string asserts a product whose name is blank.
func TestServiceRoot_ProductOmittedUnlessConfigured(t *testing.T) {
	t.Setenv(VendorEnvVar, "Dell")
	t.Setenv(ProductEnvVar, "")

	h := NewHandler(testUsername, testPassword, nil)

	assert.NotContains(t, encodeAsServed(t, h.GetServiceRoot()), "Product")
}

func TestServiceRoot_ProductIsConfigurable(t *testing.T) {
	t.Setenv(VendorEnvVar, "NVIDIA")
	t.Setenv(ProductEnvVar, "BlueField-3")

	h := NewHandler(testUsername, testPassword, nil)

	assert.Equal(t, "BlueField-3", encodeAsServed(t, h.GetServiceRoot())["Product"])
}

// The identity is read once, at construction. A client must not see the vendor
// change underneath it because something rewrote the environment mid-flight.
func TestServiceRoot_IdentityIsResolvedOnceAtStartup(t *testing.T) {
	t.Setenv(VendorEnvVar, "Dell")

	h := NewHandler(testUsername, testPassword, nil)

	t.Setenv(VendorEnvVar, "HPE")

	assert.Equal(t, "Dell", encodeAsServed(t, h.GetServiceRoot())["Vendor"],
		"vendor must be fixed at startup, not re-read per request")
}

// Vendor is a plain string, so the empty-object pruning must not disturb it,
// and reporting a vendor must not have cost us the reference fixes.
func TestServiceRoot_VendorDoesNotRegressReferences(t *testing.T) {
	t.Setenv(VendorEnvVar, "Dell")
	t.Setenv(ProductEnvVar, "PowerEdge R760")

	h := NewHandler(testUsername, testPassword, nil)
	body := encodeAsServed(t, h.GetServiceRoot())

	assert.Equal(t, "Dell", body["Vendor"])
	assert.Equal(t, "PowerEdge R760", body["Product"])
	assert.Equal(t, "RootService", body["Id"])
	assert.NotContains(t, body, "AggregationService")
	assertNoEmptyReferences(t, "ServiceRoot", body, "ProtocolFeaturesSupported")
}
