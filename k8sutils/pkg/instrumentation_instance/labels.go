package instrumentation_instance

import (
	"crypto/sha256"
	"encoding/hex"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/consts"
)

// InstrumentedAppLabelValue returns the value of the instrumented-app label for the given app name.
// Workload names can be up to 253 characters while label values are limited to 63, so the label holds
// a hash of the name and is only used for lookups. The full name is kept in the instrumented-app annotation.
func InstrumentedAppLabelValue(instrumentedAppName string) string {
	hash := sha256.Sum256([]byte(instrumentedAppName))
	return hex.EncodeToString(hash[:16])
}

// InstrumentedAppName returns the full instrumented app name of the instance.
func InstrumentedAppName(instance *odigosv1.InstrumentationInstance) (string, bool) {
	if name, ok := instance.Annotations[consts.InstrumentedAppNameAnnotation]; ok {
		return name, true
	}
	// instances created before the annotation was added hold the full name in the label.
	name, ok := instance.Labels[consts.InstrumentedAppNameLabel]
	return name, ok
}
