package anypoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// APIInstance is one API Manager API instance in an environment: an API asset version published on a Mule
// application (Mule Gateway, paired through autodiscovery) or on an Omni Gateway. The field set is what the
// anypoint-cli reads from the same endpoint (unverified beyond that).
type APIInstance struct {
	// ID is the instance id, the value an application's autodiscovery configuration names. It is a number on
	// the wire.
	ID string `json:"id"`
	// AssetID and AssetName are the Exchange asset the instance publishes; AssetName is the asset's display
	// name and may contain spaces.
	AssetID   string `json:"assetId"`
	AssetName string `json:"assetName,omitempty"`
	// AssetVersion is the asset's semantic version; ProductVersion is the API version a consumer sees (v1).
	AssetVersion   string `json:"assetVersion"`
	ProductVersion string `json:"productVersion"`
	Label          string `json:"instanceLabel,omitempty"`
	// Technology is "mule4" for an instance enforced by a Mule application and "flexGateway" for one on an
	// Omni Gateway.
	Technology string `json:"technology,omitempty"`
}

// apiPageSize is how many instances each API Manager page asks for. The pages are walked until one comes back
// empty, which holds whether the server counts the limit in assets or in instances.
const (
	apiPageSize = 100
	apiMaxPages = 100
)

func apiManagerPath(orgID, envID string) string {
	return "/apimanager/api/v1/organizations/" + url.PathEscape(orgID) + "/environments/" + url.PathEscape(envID) + "/apis"
}

// ListAPIInstances lists the API Manager instances in an environment. A listing that ran past apiMaxPages
// returns what it read with ErrIncomplete.
func (c *Client) ListAPIInstances(ctx context.Context, orgID, envID string) ([]APIInstance, error) {
	var out []APIInstance
	seen := map[string]bool{}
	for page := 0; page < apiMaxPages; page++ {
		var resp struct {
			Assets []struct {
				ExchangeAssetName string `json:"exchangeAssetName"`
				AssetID           string `json:"assetId"`
				APIs              []struct {
					ID             flexID `json:"id"`
					AssetID        string `json:"assetId"`
					AssetVersion   string `json:"assetVersion"`
					ProductVersion string `json:"productVersion"`
					InstanceLabel  string `json:"instanceLabel"`
					Technology     string `json:"technology"`
				} `json:"apis"`
			} `json:"assets"`
		}
		path := fmt.Sprintf("%s?limit=%d&offset=%d", apiManagerPath(orgID, envID), apiPageSize, page*apiPageSize)
		if err := c.do(ctx, request{method: http.MethodGet, path: path}, &resp); err != nil {
			return nil, err
		}
		if len(resp.Assets) == 0 {
			return out, nil
		}
		for _, asset := range resp.Assets {
			for _, a := range asset.APIs {
				id := string(a.ID)
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				assetID := a.AssetID
				if assetID == "" {
					assetID = asset.AssetID
				}
				out = append(out, APIInstance{
					ID: id, AssetID: assetID, AssetName: asset.ExchangeAssetName, AssetVersion: a.AssetVersion,
					ProductVersion: a.ProductVersion, Label: a.InstanceLabel, Technology: a.Technology,
				})
			}
		}
	}
	return out, fmt.Errorf("%s: more than %d pages: %w", apiManagerPath(orgID, envID), apiMaxPages, ErrIncomplete)
}

// flexID decodes an identifier that one API sends as a number and another as a string.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || bytes.Equal(b, []byte("null")):
		*f = ""
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexID(s)
	default:
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		*f = flexID(n.String())
	}
	return nil
}
