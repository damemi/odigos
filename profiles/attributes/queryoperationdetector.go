package attributes

import (
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/profiles/profile"
)

var QueryOperationDetector = profile.Profile{
	ProfileName:      common.ProfileName("query-operation-detector"),
	ShortDescription: "Detect the SQL operation name from the query text",
}
