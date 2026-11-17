package instrumentation

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/profiles/profile"
)

var InstrumentationV2Profile = profile.Profile{
	ProfileName:      common.ProfileName("instrumentation-v2"),
	ShortDescription: "Use instrumentation version v2 (Helm: instrumentationVersion=v2)",
}
