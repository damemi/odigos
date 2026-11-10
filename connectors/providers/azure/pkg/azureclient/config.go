// Package azureclient holds the shared Azure credential/plumbing used by detection: building a
// TokenCredential from the runtime credentials, verifying the configured subscription, and constructing
// the per-service ARM clients (Container Apps, AKS, Compute, Subscriptions). Keeping this
// provider-internal but detector-agnostic avoids the detector packages depending on each other.
//
// Unlike AWS (which iterates enabled regions), Azure ARM list-by-subscription APIs enumerate every
// resource group in one pager, so a single call spans the subscription.
package azureclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"

	connlog "github.com/odigos-io/odigos/connectors/base/log"
	"github.com/odigos-io/odigos/connectors/providers/azure/pkg/config"
)

var log = connlog.Subsystem("azure-client")

// LocationWildcard is the "all locations" cursor key used when a detector scans the whole subscription
// in one call (mirrors GCP's locations/- wildcard).
const LocationWildcard = "-"

// Clients builds the Azure ARM clients for a subscription from the runtime credentials. It memoizes
// nothing (the underlying clients hold connections the caller closes), only the resolved subscription
// id and the TokenCredential derived from the credentials.
type Clients struct {
	subscriptionID string
	cred           azcore.TokenCredential
}

// New builds the client factory for a subscription over the runtime credentials. When service-principal
// fields are empty the ambient DefaultAzureCredential chain is used.
func New(subscriptionID string, creds config.Credentials) (*Clients, error) {
	cred, err := tokenCredential(creds)
	if err != nil {
		return nil, err
	}
	return &Clients{subscriptionID: subscriptionID, cred: cred}, nil
}

// tokenCredential selects ClientSecretCredential when all three SP fields are set; otherwise
// DefaultAzureCredential (Workload Identity / Managed Identity / env / CLI).
func tokenCredential(creds config.Credentials) (azcore.TokenCredential, error) {
	if creds.TenantID != "" && creds.ClientID != "" && creds.ClientSecret != "" {
		return azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
	}
	return azidentity.NewDefaultAzureCredential(nil)
}

// SubscriptionID returns the configured Azure subscription id (the connector boundary).
func (c *Clients) SubscriptionID() string { return c.subscriptionID }

// Cred returns the shared TokenCredential.
func (c *Clients) Cred() azcore.TokenCredential { return c.cred }

// ContainerApps builds a Container Apps client. The caller may discard it after the scan.
func (c *Clients) ContainerApps() (*armappcontainers.ContainerAppsClient, error) {
	return armappcontainers.NewContainerAppsClient(c.subscriptionID, c.cred, nil)
}

// AKSClusters builds an AKS managed-clusters client.
func (c *Clients) AKSClusters() (*armcontainerservice.ManagedClustersClient, error) {
	return armcontainerservice.NewManagedClustersClient(c.subscriptionID, c.cred, nil)
}

// VirtualMachines builds a Compute Virtual Machines client.
func (c *Clients) VirtualMachines() (*armcompute.VirtualMachinesClient, error) {
	return armcompute.NewVirtualMachinesClient(c.subscriptionID, c.cred, nil)
}

// VerifySubscription enforces the connector's Azure subscription boundary: it reads the configured
// subscription through the runtime credentials (Microsoft.Resources/subscriptions/read) and fails unless
// it resolves. An empty subscription id is itself a misconfiguration and is rejected.
func (c *Clients) VerifySubscription(ctx context.Context) error {
	if c.subscriptionID == "" {
		return fmt.Errorf("no Azure subscription id configured (ODIGOS_AZURE_SUBSCRIPTION_ID); cannot enforce the connector subscription boundary")
	}
	client, err := armsubscriptions.NewClient(c.cred, nil)
	if err != nil {
		return fmt.Errorf("subscriptions client: %w", err)
	}
	resp, err := client.Get(ctx, c.subscriptionID, nil)
	if err != nil {
		return fmt.Errorf("get subscription %q: %w", c.subscriptionID, err)
	}
	if resp.SubscriptionID != nil {
		got := strings.ToLower(*resp.SubscriptionID)
		want := strings.ToLower(c.subscriptionID)
		if got != want {
			return fmt.Errorf("azure subscription boundary violation: credentials resolve to subscription %q but the connector is configured for subscription %q", *resp.SubscriptionID, c.subscriptionID)
		}
	}
	log.Info("azure subscription boundary verified", "subscription", c.subscriptionID)
	return nil
}

// ResourceGroupFromID extracts the resource group name from an ARM resource ID.
func ResourceGroupFromID(resourceID string) string {
	parts := strings.Split(strings.Trim(resourceID, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}
