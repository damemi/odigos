package testconnection

import (
	"context"
	"errors"
	"fmt"

	"github.com/odigos-io/odigos/connectors/anypoint"
	"github.com/odigos-io/odigos/connectors/spec"
)

// Discovery type names, matching the discovery types declared in connectors/spec/data/mulesoft.yaml.
const (
	typeMuleCloudHub2    = "mulesoft.cloudhub2-app"
	typeMulePrivateSpace = "mulesoft.private-space-app"
	typeMuleRTF          = "mulesoft.rtf-app"
	typeMuleHybrid       = "mulesoft.hybrid-app"
	typeMuleOmniGateway  = "mulesoft.omni-gateway"
)

// Credential keys, matching the auth field names declared in connectors/spec/data/mulesoft.yaml.
const (
	credMuleClientID     = "clientId"
	credMuleClientSecret = "clientSecret"
)

// MuleSoft connection check names.
const (
	CheckMuleAuthToken             CheckName = "auth_token"
	CheckMuleOrganizationMatch     CheckName = "organization_match"
	CheckMuleEnvironments          CheckName = "environments_list"
	CheckMuleDiscoveryCloudHub2    CheckName = "discovery_cloudhub2"
	CheckMuleDiscoveryPrivateSpace CheckName = "discovery_private_space"
	CheckMuleDiscoveryRTF          CheckName = "discovery_rtf"
	CheckMuleDiscoveryHybrid       CheckName = "discovery_hybrid"
	CheckMuleDiscoveryGateway      CheckName = "discovery_omni_gateway"
)

func init() { Register(muleSoftTester{}) }

// muleSoftTester validates a Connected App with read-only Anypoint calls. Whether the organization's
// subscription includes Direct Telemetry Stream cannot be read from the API, so no check claims it; the setup
// prerequisites state it instead.
//
// The API host is only ever one of the two control planes. Nothing a caller sends can change it: test
// connection runs in Central's backend on behalf of any signed-in user, so a request-supplied host would let
// them aim the backend's requests (and the Connected App credentials) at any URL. baseURL exists for tests.
type muleSoftTester struct{ baseURL string }

func (muleSoftTester) Provider() string { return "mulesoft" }

func (t muleSoftTester) Test(ctx context.Context, in Input) (*Result, error) {
	res := &Result{}
	s := spec.MustGet("mulesoft")
	fail := func(name CheckName, display, msg, remediation string) (*Result, error) {
		res.add(CheckResult{Name: name, DisplayName: display, Passed: false, Message: msg, Remediation: remediation})
		res.finalize()
		return res, nil
	}

	chain, err := s.Auth.Resolve(in.Credentials)
	if err != nil {
		return fail(CheckMuleAuthToken, "Authentication", err.Error(),
			"provide the Connected App's client ID and client secret")
	}
	orgID := in.ExpectedAccountID
	if orgID == "" {
		return fail(CheckMuleAuthToken, "Authentication", "no Anypoint organization id configured",
			"set the connector's organization id")
	}
	baseURL, err := anypoint.BaseURLFor(in.BootstrapRegion, t.baseURL)
	if err != nil {
		return fail(CheckMuleAuthToken, "Authentication", err.Error(), "choose the US or EU control plane")
	}
	client := anypoint.New(anypoint.Config{
		BaseURL:      baseURL,
		ClientID:     in.Credentials[credMuleClientID],
		ClientSecret: in.Credentials[credMuleClientSecret],
	})

	// 1. The client-credentials exchange proves the pair is valid for this control plane.
	if _, err := client.Token(ctx); err != nil {
		return fail(CheckMuleAuthToken, "Authentication", anypoint.Summarize(err),
			"verify the client ID and secret, that the Connected App uses the client-credentials grant, and that it "+
				"belongs to the selected control plane ("+in.BootstrapRegion+")")
	}
	id, err := client.Me(ctx)
	if err != nil {
		return fail(CheckMuleAuthToken, "Authentication", anypoint.Summarize(err),
			"verify the Connected App is active")
	}
	res.AccountID = id.OrgID
	res.Identity = id.ClientName
	res.add(CheckResult{Name: CheckMuleAuthToken, DisplayName: "Authentication", Passed: true,
		Message: fmt.Sprintf("authenticated to organization %s (via %s)", id.OrgID, authMethodNames(chain))})

	// 2. Organization match. A Connected App created in a parent organization can be granted scopes in a child
	// business group, so a mismatch is not proof of a mistake — but the environments listed below are then the
	// child's, and the check says which organization the credentials belong to.
	match := id.OrgID == orgID
	c := CheckResult{Name: CheckMuleOrganizationMatch, DisplayName: "Organization match", Passed: true}
	if match {
		c.Message = fmt.Sprintf("Connected App belongs to organization %s", orgID)
	} else {
		c.Message = fmt.Sprintf("Connected App belongs to organization %s; it will manage business group %s through its scopes", id.OrgID, orgID)
	}
	res.add(c)

	// 3. Environments: a Connected App with no environment in its scopes sees nothing to discover.
	// A truncated list still proves the scope, which is all a connection check asks.
	envs, err := client.Environments(ctx, orgID)
	if errors.Is(err, anypoint.ErrIncomplete) {
		err = nil
	}
	if err != nil {
		res.add(discoveryCheck(CheckResult{Name: CheckMuleEnvironments, DisplayName: "Environments",
			Remediation: "grant the Connected App the View Environment scope in business group " + orgID},
			err, anypoint.Summarize, anypoint.IsAuthErr))
		res.finalize()
		return res, nil
	}
	if len(envs) == 0 {
		return fail(CheckMuleEnvironments, "Environments", "the Connected App can see no environments in organization "+orgID,
			"add the environments Odigos should manage to the Connected App's scopes")
	}
	res.add(CheckResult{Name: CheckMuleEnvironments, DisplayName: "Environments", Passed: true,
		Message: fmt.Sprintf("%d environment(s) visible", len(envs))})

	active := map[string]bool{}
	for _, t := range s.ActiveDiscoveryTypes(in.Capabilities) {
		active[t.Name] = true
	}
	if active[typeMuleCloudHub2] || active[typeMulePrivateSpace] || active[typeMuleRTF] {
		envName, err := probeEnvironments(envs, func(envID string) error {
			_, err := client.ListDeployments(ctx, orgID, envID)
			if errors.Is(err, anypoint.ErrIncomplete) {
				return nil
			}
			return err
		})
		if active[typeMuleCloudHub2] {
			res.add(discoveryCheck(CheckResult{Name: CheckMuleDiscoveryCloudHub2, DisplayName: "CloudHub 2.0 discovery permissions",
				Message: "deployments readable in " + envName, Remediation: "grant the Read Applications scope in every environment Odigos should manage"},
				err, anypoint.Summarize, anypoint.IsAuthErr))
		}
		// A private space is told apart from a Runtime Fabric by the targets list, but a target missing from it is
		// classified as a private space anyway, so reading deployments is all private-space discovery needs.
		if active[typeMulePrivateSpace] {
			res.add(discoveryCheck(CheckResult{Name: CheckMuleDiscoveryPrivateSpace, DisplayName: "CloudHub 2.0 private space discovery permissions",
				Message: "deployments readable in " + envName, Remediation: "grant the Read Applications scope in every environment Odigos should manage"},
				err, anypoint.Summarize, anypoint.IsAuthErr))
		}
		if active[typeMuleRTF] {
			if err == nil {
				_, err = client.Targets(ctx, orgID)
			}
			res.add(discoveryCheck(CheckResult{Name: CheckMuleDiscoveryRTF, DisplayName: "Runtime Fabric discovery permissions",
				Message: "deployments and targets readable", Remediation: "grant the Read Applications and Read Runtime Fabrics scopes"},
				err, anypoint.Summarize, anypoint.IsAuthErr))
		}
	}
	if active[typeMuleHybrid] {
		envName, err := probeEnvironments(envs, func(envID string) error {
			_, err := client.ListHybridApplications(ctx, orgID, envID)
			if errors.Is(err, anypoint.ErrIncomplete) {
				err = nil
			}
			if err == nil {
				_, err = client.ListHybridServers(ctx, orgID, envID)
			}
			return err
		})
		res.add(discoveryCheck(CheckResult{Name: CheckMuleDiscoveryHybrid, DisplayName: "Hybrid discovery permissions",
			Message: "hybrid applications and servers readable in " + envName, Remediation: "grant the Read Applications and Read Servers scopes"},
			err, anypoint.Summarize, anypoint.IsAuthErr))
	}
	if active[typeMuleOmniGateway] {
		envName, err := probeEnvironments(envs, func(envID string) error {
			_, err := client.ListGateways(ctx, orgID, envID)
			if errors.Is(err, anypoint.ErrIncomplete) {
				err = nil
			}
			return err
		})
		res.add(discoveryCheck(CheckResult{Name: CheckMuleDiscoveryGateway, DisplayName: "Omni Gateway discovery permissions",
			Message: "gateways readable in " + envName, Remediation: "grant the Read Servers scope in every environment Odigos should manage"},
			err, anypoint.Summarize, anypoint.IsAuthErr))
	}

	res.finalize()
	return res, nil
}

// maxProbedEnvironments bounds how many environments one permission check tries, so a Connected App missing a
// scope everywhere fails in a few calls rather than one per environment of a large organization.
const maxProbedEnvironments = 5

// probeEnvironments runs probe against the visible environments in order until one passes, returning that
// environment's name. One readable environment proves the scope; scanning them all is discovery's job. Trying
// more than the first matters because the setup guide has customers leave environments Odigos should not
// manage out of the Connected App's scopes, and the environment list may still show them. When none passes,
// the last error is returned.
func probeEnvironments(envs []anypoint.Environment, probe func(envID string) error) (string, error) {
	var err error
	for i, env := range envs {
		if i == maxProbedEnvironments {
			break
		}
		if err = probe(env.ID); err == nil {
			return env.Name, nil
		}
	}
	return "", err
}
