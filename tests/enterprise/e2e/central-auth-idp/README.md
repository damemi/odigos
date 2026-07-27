# Central Auth IdP E2E (standalone)

Standalone auth coverage for Central IdP setup. **Not part of the Cypress suite.**

## What it validates

1. **Discovery URLs** for Google, Azure, and Okta  
   Exercises `getOIDCDiscoveryEndpoints` in `central-backend/internal/keycloak/admin.go` via Go tests (live HTTP).  
   Okta live discovery requires `E2E_OKTA_TENANT_ID` (org subdomain).

2. **Create IdP** for `GOOGLE` / `AZURE` / `OKTA` × `OIDC` / `SAML`  
   One case at a time. Keycloak (and central-backend) are restarted between cases so
   ephemeral H2 is wiped and `odigos-central` is recreated before create/login.

3. **Login using the created IdP**
   - **OIDC:** authorization-code login against in-cluster `mock-oauth2-server`
   - **SAML:** broker challenge redirects to the configured SSO endpoint

## Run locally

Prerequisites: kind cluster, sibling OSS repo with `helm/odigos-central`, central images loaded, `ODIGOS_API_KEY` set.

```bash
# Optional for live Okta discovery:
export E2E_OKTA_TENANT_ID=dev-xxxxx

chainsaw test tests/e2e/central-auth-idp
```

Or only the discovery unit tests:

```bash
cd central-backend
go test ./internal/keycloak/ -count=1 -run 'TestResolveDiscoveryURL|TestGetOIDCDiscoveryEndpoints_|TestGetIDPConfig_PopulatesEndpointsFromLiveDiscovery'
```

## Env vars

| Variable | Purpose |
|---|---|
| `ODIGOS_API_KEY` / `ODIGOS_ONPREM_TOKEN` | Central helm install |
| `IMAGE_TAG` | Central image tag (CI sets `e2e-test`) |
| `E2E_OKTA_TENANT_ID` | Okta org subdomain for live discovery |
| `E2E_AZURE_TENANT_ID` | Optional Azure tenant GUID for issuer rewrite |
| `DISCOVERY_MODE=mock` | Skip live vendor discovery during create (mock only) |
CI trigger 2026-07-16T14:19:21Z
