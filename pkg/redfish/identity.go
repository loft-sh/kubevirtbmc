package redfish

import (
	"os"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	// VendorEnvVar and ProductEnvVar name the environment variables that set
	// what ServiceRoot reports for Vendor and Product.
	//
	// These are read here rather than in cmd/virtbmc/main.go, where
	// BMC_USERNAME and BMC_PASSWORD are read. The agent binary's flags,
	// arguments and entrypoint have to stay byte-identical so the image
	// remains a drop-in swap, so main.go is left alone. The deployment story
	// is unchanged either way: both are plain container environment variables
	// that a chart can inject and that a pod restart re-reads.
	VendorEnvVar  = "BMC_VENDOR"
	ProductEnvVar = "BMC_PRODUCT"

	// defaultVendor is a PLACEHOLDER. Redfish leaves Vendor free-form, but
	// clients gate on it: NVIDIA NICo's site-explorer rejects a ServiceRoot
	// whose vendor it does not recognize ("did not report a recognized
	// vendor"), matching the lowercased string against a fixed set, and each
	// recognized vendor pulls in its own vendor-specific handling downstream.
	// Which identity costs least is a question about the client, not about
	// virtbmc, so it is settled by configuration rather than by this constant.
	defaultVendor = "Supermicro"
)

// serviceRootIdentity is what ServiceRoot reports about who made this BMC.
type serviceRootIdentity struct {
	// vendor is always set; ServiceRoot must report a vendor for clients that
	// gate on one.
	vendor string
	// product is empty when unset, in which case ServiceRoot omits it. Redfish
	// treats an absent property as "not reported", which is honest, whereas an
	// empty string claims a product whose name is blank.
	product string
}

// identityFromEnv resolves the identity ServiceRoot reports from the
// environment, falling back to the placeholder vendor when unset.
//
// Values are trimmed: these arrive by way of a Secret or a chart value often
// enough to pick up a trailing newline, and a client that matches the vendor
// string exactly would not recognize "Supermicro\n".
func identityFromEnv() serviceRootIdentity {
	identity := serviceRootIdentity{
		vendor:  strings.TrimSpace(os.Getenv(VendorEnvVar)),
		product: strings.TrimSpace(os.Getenv(ProductEnvVar)),
	}

	if identity.vendor == "" {
		identity.vendor = defaultVendor
	}

	return identity
}

// log records the resolved identity once at startup. A client that rejects the
// BMC over its vendor reports only that the vendor was unrecognized, never
// which value it saw, so the value has to be greppable from this side.
func (i serviceRootIdentity) log() {
	entry := logrus.WithField("vendor", i.vendor)
	if i.product != "" {
		entry = entry.WithField("product", i.product)
	}
	entry.Info("ServiceRoot identity resolved")
}
