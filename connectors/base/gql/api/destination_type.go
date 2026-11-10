package gqlapi

import (
	"context"
	"fmt"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	centralapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

// DestinationTypeAPI maps GraphQL DestinationType queries to the connector's destination catalog.
type DestinationTypeAPI struct {
	Central centralapi.CentralInterface
}

func (a *DestinationTypeAPI) DestinationTypes(ctx context.Context) ([]*model.DestinationType, error) {
	catalog := a.Central.DestinationCatalog()
	if live, ok := a.Central.(interface {
		DestinationCatalogFor(context.Context) (domaindest.Catalog, error)
	}); ok {
		var err error
		catalog, err = live.DestinationCatalogFor(ctx)
		if err != nil {
			return nil, fmt.Errorf("destinationTypes: %w", err)
		}
	}
	out, err := catalog.SupportedCatalogDestinationTypes()
	if err != nil {
		return nil, fmt.Errorf("destinationTypes: %w", err)
	}
	return out, nil
}
