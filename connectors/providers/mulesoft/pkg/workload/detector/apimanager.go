package detector

import (
	"strings"

	"github.com/odigos-io/odigos/connectors/anypoint"
	store "github.com/odigos-io/odigos/connectors/providers/mulesoft/pkg/workload/store"
)

// PairedAPI returns the API Manager instance an application pairs with through autodiscovery, or nil.
//
// Autodiscovery names its instance by id, and an application supplies that id through one of its own
// properties, conventionally api.id — the name is the application's choice, so any property whose name ends
// in api.id (or apiId, api_id, api-id) counts. The value has to be an instance in the application's own
// environment: a stray number in some other property, or an instance from elsewhere, pairs with nothing. A
// secure property's value cannot be read, so an application that keeps its api.id secure is not paired.
func PairedAPI(props map[string]string, apis []anypoint.APIInstance) *store.APIRef {
	if len(props) == 0 || len(apis) == 0 {
		return nil
	}
	byID := make(map[string]anypoint.APIInstance, len(apis))
	for _, a := range apis {
		byID[a.ID] = a
	}
	for k, v := range props {
		if !isAPIIDKey(k) {
			continue
		}
		if a, ok := byID[strings.TrimSpace(v)]; ok {
			return &store.APIRef{ID: a.ID, AssetID: a.AssetID, Name: a.AssetName, Version: apiVersion(a), Label: a.Label}
		}
	}
	return nil
}

func isAPIIDKey(k string) bool {
	k = strings.ToLower(k)
	for _, suffix := range []string{"api.id", "apiid", "api_id", "api-id"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// apiVersion is the version a consumer of the API sees, falling back to the asset's own version.
func apiVersion(a anypoint.APIInstance) string {
	if a.ProductVersion != "" {
		return a.ProductVersion
	}
	return a.AssetVersion
}
