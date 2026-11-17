package attributes

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/profiles/profile"
)

var CategoryAttributesProfile = profile.Profile{
	ProfileName:      common.ProfileName("category-attributes"),
	ShortDescription: "Add category attributes to the spans",
}
