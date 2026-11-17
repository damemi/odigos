package instrumentation

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/profiles/profile"
)

var DisableGinProfile = profile.Profile{
	ProfileName:      common.ProfileName("disable-gin"),
	ShortDescription: "Disable gin-gonic/gin instrumentation",
}

var DisableGinNewProfile = profile.Profile{
	ProfileName:      common.ProfileName("disable-golang-gin"),
	ShortDescription: "Disable gin-gonic/gin instrumentation in Go",
}
