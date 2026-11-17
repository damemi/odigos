package instrumentation

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/profiles/profile"
)

var LegacyRubyProfile = profile.Profile{
	ProfileName:      common.ProfileName("legacy-ruby-instrumentation"),
	ShortDescription: "Enable native instrumentation for Ruby 2.7 (requires odiglet.extended)",
}
