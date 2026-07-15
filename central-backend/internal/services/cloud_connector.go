package services

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/odigos-io/central-backend/internal/k8s"
	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CloudConnectorService owns creation and listing of OdigosCloudConnector CRs
// (and their credentials Secrets) in the central cluster.
type CloudConnectorService struct {
	k8sClient *k8s.Client
	namespace string
}

// CreateConnectorParams is the resolved, provider-agnostic input for creating a
// connector. The resolver builds it after a successful test-connection.
type CreateConnectorParams struct {
	Provider           string
	DisplayName        string
	AccountID            string
	AccountDisplayName   string
	Workloads            []cloudconnectorsv1.CloudConnectorWorkloadCapability
	ComputePlatforms     []cloudconnectorsv1.CloudConnectorComputePlatformCapability
	// SecretData holds the credential values keyed by the provider spec's auth
	// field names (the keys the connector runtime references via Secret keyRefs).
	SecretData map[string]string
}

func NewCloudConnectorService(k8sClient *k8s.Client) *CloudConnectorService {
	return &CloudConnectorService{
		k8sClient: k8sClient,
		namespace: GetCurrentNamespace(),
	}
}

// CreateConnector upserts the credentials Secret and the OdigosCloudConnector CR,
// returning the CR name. Connectors are keyed by provider+account, so submitting
// the same account again updates the existing connector instead of failing.
func (svc *CloudConnectorService) CreateConnector(ctx context.Context, params CreateConnectorParams) (string, error) {
	if svc.k8sClient == nil {
		return "", fmt.Errorf("k8s client is not available")
	}

	name := connectorName(params.Provider, params.AccountID)
	secretName := name + "-credentials"

	if len(params.SecretData) > 0 {
		if err := svc.k8sClient.UpsertSecret(ctx, svc.namespace, secretName, params.SecretData); err != nil {
			return "", err
		}
	}

	cc := &cloudconnectorsv1.OdigosCloudConnector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: svc.namespace,
		},
		Spec: cloudconnectorsv1.OdigosCloudConnectorSpec{
			Provider:    params.Provider,
			DisplayName: params.DisplayName,
			Account: cloudconnectorsv1.CloudConnectorAccount{
				ID:          params.AccountID,
				DisplayName: params.AccountDisplayName,
			},
			CredentialsSecretRef: cloudconnectorsv1.CloudConnectorCredentialsSecretRef{
				Name: secretName,
			},
			Workloads:        params.Workloads,
			ComputePlatforms: params.ComputePlatforms,
		},
	}

	if err := svc.k8sClient.UpsertCloudConnector(ctx, cc); err != nil {
		return "", err
	}

	return name, nil
}

// ListConnectors returns the OdigosCloudConnector CRs in the connector namespace.
func (svc *CloudConnectorService) ListConnectors(ctx context.Context) ([]cloudconnectorsv1.OdigosCloudConnector, error) {
	if svc.k8sClient == nil {
		return nil, fmt.Errorf("k8s client is not available")
	}
	return svc.k8sClient.ListCloudConnectors(ctx, svc.namespace)
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// connectorName builds a DNS-1123-safe CR/Secret base name from the provider and
// account id (e.g. "aws-123456789012").
func connectorName(provider, accountID string) string {
	raw := strings.ToLower(fmt.Sprintf("%s-%s", provider, accountID))
	raw = invalidNameChars.ReplaceAllString(raw, "-")
	raw = strings.Trim(raw, "-")
	if raw == "" {
		raw = "cloud-connector"
	}
	if len(raw) > 200 {
		raw = strings.Trim(raw[:200], "-")
	}
	return raw
}
