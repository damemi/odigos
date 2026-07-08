package gqlapi

import (
	"context"
	"fmt"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	centralapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// DestinationTypeAPI maps GraphQL DestinationType queries to the connector's destination catalog.
type DestinationTypeAPI struct {
	Central centralapi.CentralInterface
}

func (a *DestinationTypeAPI) DestinationTypes(context.Context) ([]*model.DestinationType, error) {
	out, err := a.Central.DestinationCatalog().SupportedCatalogDestinationTypes()
	if err != nil {
		return nil, fmt.Errorf("destinationTypes: %w", err)
	}
	return out, nil
}
