package testconnection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"

	"github.com/odigos-io/odigos/connectors/spec"
)

// Discovery type names, matching the discovery types declared in connectors/spec/data/azure.yaml.
const (
	typeContainerApps = "azure.container-apps"
	typeAKS           = "azure.aks"
	typeVM            = "azure.vm"
)

// Credential keys, matching the auth field names declared in connectors/spec/data/azure.yaml.
const (
	credTenantID     = "tenantId"
	credClientID     = "clientId"
	credClientSecret = "clientSecret"
)

// Azure connection check names.
const (
	CheckAzureAuthIdentity           CheckName = "auth_identity"
	CheckAzureSubscriptionMatch      CheckName = "subscription_match"
	CheckAzureDiscoveryContainerApps CheckName = "discovery_container_apps"
	CheckAzureDiscoveryAKS           CheckName = "discovery_aks"
	CheckAzureDiscoveryVM            CheckName = "discovery_vm"
)

func init() { Register(azureTester{}) }

// azureTester validates Azure connector credentials with read-only probes (subscription access +
// discovery permissions) using in-memory credentials only.
type azureTester struct{}

func (azureTester) Provider() string { return "azure" }

// Test executes the read-only checks and returns a per-check report. It returns a non-nil error only
// for unexpected internal failures; expected outcomes (bad credentials, missing permissions) are
// reported as failed checks so the UI can render per-check guidance.
func (azureTester) Test(ctx context.Context, in Input) (*Result, error) {
	res := &Result{}
	azureSpec := spec.MustGet("azure")

	// Resolve which auth method the provided credentials select, validated against the spec's auth
	// definition. This rejects incomplete/ambiguous credential sets before contacting Azure.
	chain, err := azureSpec.Auth.Resolve(in.Credentials)
	if err != nil {
		res.add(CheckResult{
			Name: CheckAzureAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     err.Error(),
			Remediation: "provide a valid set of credentials for one of the supported authentication methods",
		})
		res.finalize()
		return res, nil
	}

	cred, err := azureTokenCredential(in.Credentials)
	if err != nil {
		res.add(CheckResult{
			Name: CheckAzureAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     "failed to initialize Azure credentials: " + summarizeAzureErr(err),
			Remediation: "verify the provided service principal tenant/client/secret, or configure workload identity",
		})
		res.finalize()
		return res, nil
	}

	// 1. Authentication + subscription access (Microsoft.Resources/subscriptions/read).
	subscriptionID := in.ExpectedAccountID
	if subscriptionID == "" {
		res.add(CheckResult{
			Name: CheckAzureAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     "no Azure subscription id configured",
			Remediation: "set the connector's subscription id",
		})
		res.finalize()
		return res, nil
	}
	subClient, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		res.add(CheckResult{
			Name: CheckAzureAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     "failed to initialize Azure subscriptions client: " + summarizeAzureErr(err),
			Remediation: "verify the provided credentials",
		})
		res.finalize()
		return res, nil
	}

	sub, err := subClient.Get(ctx, subscriptionID, nil)
	if err != nil {
		res.add(CheckResult{
			Name: CheckAzureAuthIdentity, DisplayName: "Authentication", Passed: false,
			Message:     summarizeAzureErr(err),
			Remediation: "ensure the credentials are valid and granted Microsoft.Resources/subscriptions/read on the subscription",
		})
		res.finalize()
		return res, nil
	}
	if sub.SubscriptionID != nil {
		res.AccountID = *sub.SubscriptionID
	}
	if sub.DisplayName != nil {
		res.Identity = *sub.DisplayName
	}
	res.add(CheckResult{
		Name: CheckAzureAuthIdentity, DisplayName: "Authentication", Passed: true,
		Message: fmt.Sprintf("authenticated to subscription %s (via %s)", res.AccountID, authMethodNames(chain)),
	})

	// 2. Subscription match.
	match := res.AccountID == "" || strings.EqualFold(res.AccountID, subscriptionID)
	c := CheckResult{Name: CheckAzureSubscriptionMatch, DisplayName: "Subscription match", Passed: match}
	if match {
		c.Message = fmt.Sprintf("credentials can access subscription %s", subscriptionID)
	} else {
		c.Message = fmt.Sprintf("credentials resolve to subscription %s, expected %s", res.AccountID, subscriptionID)
		c.Remediation = "use credentials for the configured subscription, or correct the configured subscription id"
	}
	res.add(c)

	// Only probe discovery if the customer enabled at least one discovery capability.
	active := map[string]bool{}
	for _, t := range azureSpec.ActiveDiscoveryTypes(in.Capabilities) {
		active[t.Name] = true
	}
	if len(active) == 0 {
		res.finalize()
		return res, nil
	}

	if active[typeContainerApps] {
		err := probeContainerApps(ctx, subscriptionID, cred)
		res.add(discoveryCheck(CheckResult{Name: CheckAzureDiscoveryContainerApps, DisplayName: "Container Apps discovery permissions",
			Message: "Microsoft.App/containerApps/read allowed", Remediation: "grant " + permissionsFor(azureSpec, in.Capabilities, "Microsoft.App/")}, err, summarizeAzureErr, isAzureAuthErr))
	}
	if active[typeAKS] {
		err := probeAKS(ctx, subscriptionID, cred)
		res.add(discoveryCheck(CheckResult{Name: CheckAzureDiscoveryAKS, DisplayName: "AKS discovery permissions",
			Message: "Microsoft.ContainerService/managedClusters/read allowed", Remediation: "grant " + permissionsFor(azureSpec, in.Capabilities, "Microsoft.ContainerService/")}, err, summarizeAzureErr, isAzureAuthErr))
	}
	if active[typeVM] {
		err := probeVM(ctx, subscriptionID, cred)
		res.add(discoveryCheck(CheckResult{Name: CheckAzureDiscoveryVM, DisplayName: "Virtual Machine discovery permissions",
			Message: "Microsoft.Compute/virtualMachines/read allowed", Remediation: "grant " + permissionsFor(azureSpec, in.Capabilities, "Microsoft.Compute/")}, err, summarizeAzureErr, isAzureAuthErr))
	}

	res.finalize()
	return res, nil
}

// azureTokenCredential builds a TokenCredential from in-memory credentials. Empty credentials use
// DefaultAzureCredential (workload identity).
func azureTokenCredential(creds map[string]string) (azcore.TokenCredential, error) {
	tenant := creds[credTenantID]
	clientID := creds[credClientID]
	secret := creds[credClientSecret]
	if tenant != "" && clientID != "" && secret != "" {
		return azidentity.NewClientSecretCredential(tenant, clientID, secret, nil)
	}
	return azidentity.NewDefaultAzureCredential(nil)
}

// probeContainerApps issues a single page of ListBySubscription (the minimal read the discovery loop performs).
func probeContainerApps(ctx context.Context, subscriptionID string, cred azcore.TokenCredential) error {
	client, err := armappcontainers.NewContainerAppsClient(subscriptionID, cred, nil)
	if err != nil {
		return err
	}
	pager := client.NewListBySubscriptionPager(nil)
	if !pager.More() {
		return nil
	}
	_, err = pager.NextPage(ctx)
	return err
}

// probeAKS issues a single page of ManagedClusters.List.
func probeAKS(ctx context.Context, subscriptionID string, cred azcore.TokenCredential) error {
	client, err := armcontainerservice.NewManagedClustersClient(subscriptionID, cred, nil)
	if err != nil {
		return err
	}
	pager := client.NewListPager(nil)
	if !pager.More() {
		return nil
	}
	_, err = pager.NextPage(ctx)
	return err
}

// probeVM issues a single page of VirtualMachines.ListAll.
func probeVM(ctx context.Context, subscriptionID string, cred azcore.TokenCredential) error {
	client, err := armcompute.NewVirtualMachinesClient(subscriptionID, cred, nil)
	if err != nil {
		return err
	}
	pager := client.NewListAllPager(nil)
	if !pager.More() {
		return nil
	}
	_, err = pager.NextPage(ctx)
	return err
}

// isAzureAuthErr reports whether an Azure error is an authorization/permission failure.
func isAzureAuthErr(err error) bool {
	if err == nil {
		return false
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		if respErr.StatusCode == http.StatusUnauthorized || respErr.StatusCode == http.StatusForbidden {
			return true
		}
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "authorizationfailed") || strings.Contains(s, "permission") ||
		strings.Contains(s, "forbidden") || strings.Contains(s, "unauthorized") ||
		strings.Contains(s, "access denied") || strings.Contains(s, "not authorized")
}

// summarizeAzureErr returns a concise, secret-free description of an Azure error (mirrors
// summarizeGCPErr: status/code + message, never raw response bodies).
func summarizeAzureErr(err error) string {
	if err == nil {
		return ""
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		code := respErr.ErrorCode
		if code == "" {
			code = http.StatusText(respErr.StatusCode)
		}
		message := fmt.Sprintf("%d: %s", respErr.StatusCode, code)
		if respErr.RawResponse != nil {
			// Azure stores its diagnostic message in the response JSON. Keep that
			// field and request ID rather than exposing headers or the raw body.
			payload, readErr := runtime.Payload(respErr.RawResponse)
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if readErr == nil && json.Unmarshal(payload, &body) == nil && body.Error.Message != "" {
				message += ": " + body.Error.Message
			}
			if id := respErr.RawResponse.Header.Get("x-ms-request-id"); id != "" {
				message += " (request ID: " + id + ")"
			}
		}
		return message
	}
	return err.Error()
}
