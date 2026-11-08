# Odigos Enterprise documentation

Source for **https://enterprise.docs.odigos.io**. This protected site documents Enterprise Kubernetes, Odigos Central, VM Agent, and Cloud Connectors. The public site at https://docs.odigos.io documents the Kubernetes Agent Open Source edition, plus overview pages for Odigos Central, the VM Agent, Cloud Connectors, the Enterprise MCP server, and continuous profiling.

Both sites use the same Mintlify theme, MDX components, logos, and navigation conventions. This site uses [Mintlify's offline export](https://www.mintlify.com/docs/deploy/export) and a Go authentication server, so a Mintlify Enterprise subscription is not required. Pagefind provides search from the protected build; no hosted search index contains this site's content.

## Editing and building

Use Node 22 or later and pnpm 11.19.0:

```sh
cd docs
pnpm install --frozen-lockfile
pnpm format:check
pnpm check
pnpm dev
pnpm build
```

`pnpm dev` is an **unauthenticated local authoring preview**; do not expose it publicly. The production image serves only the compiled `build/` through `docs-server` from the odigos repo's `odigosauth/cmd/docs-server` (version pinned in `server/go.mod`). The build copies MDX into an isolated staging directory before export, excludes raw sources and the export's example server, checks every page is exported, expands legacy redirects, and creates a local search index. Internet access is required during export to obtain the Mintlify renderer; runtime serving does not require a Mintlify account or API. The existing theme still references public font and icon CDNs.

Add each new page to `docs.json`. Use a hidden group for reference pages that should be reachable but absent from the sidebar. The build fails if any documentation page is missing from the export. Enterprise Kubernetes, Central, VM Agent, and Cloud Connectors stay in this site's navigation. Keep the link to the public docs visible.

`node scripts/build.mjs --reuse-export` rebuilds search and browser assets from the previous export for local iteration. Do not use it after editing MDX or in CI/release builds.

### Application source and formatting

`assets/docs-runtime.js` and `assets/docs-runtime.css` are handwritten source and belong in Git. They provide session-expiry handling, authenticated navigation, local search, and sign-out controls around the exported Mintlify pages. `scripts/build.mjs` copies them into the build; they are not generated or minified bundles. The login template and styles live in the odigos repo under `odigosauth/docslogin`.

Generated site files live in `build/`, `.build-source/`, and `export.zip`, all ignored by Git. This includes the Mintlify bundles and Pagefind index. Do not commit or edit those build artifacts.

Run `pnpm format` to format the handwritten JavaScript, CSS, Go HTML template, and configuration files with Prettier. Use `black --config docs/pyproject.toml docs/deploy docs/scripts` from the repository root for Python (Black 26.5.1), and `gofmt` for Go. The workflow checks formatting before deployment. Formatting is scoped to application code and configuration; generated documentation snippets retain their generator's output.

### Generated documentation

CLI, CRD, destination, instrumentation, and RBAC snippets are generated in the public `odigos` repository under `snippets/oss`. Run `make cli-docs`, `cd api && make docgen`, `make rbac-docs`, and `cd docs && python sync-dest-doc.py` there first. Copy changed files into this repo's matching `snippets/enterprise` paths and review the diff. Pyroscope stays in this repo only. The OBI guide exists here and on the public site. Run this site's checks after syncing.

## Authentication

The password is the **generated on-prem enterprise API key** from `onprem-token-generator`. `odigosauth.VerifyOnpremTokenWithExpiry` reuses the production RSA public key and claim validation: RS256 signature, issuer `https://odigos.io`, subject `https://odigos.io/onprem`, nonempty customer audience, required future expiration, and `nbf` when supplied. Any customer/profile is accepted. Opaque legacy cloud tokens are not generated enterprise license keys and are not accepted.

The server exchanges a valid key for a separate signed session cookie. The API key is not placed in cookies, URLs, browser storage, or application logs. `__Host-odigos-docs` is Secure, HttpOnly, host-only, and SameSite=Lax. Login and logout require a same-origin POST. Sessions last at most 24 hours and **never beyond the key's expiration**. Every page, script, image, search fragment, and redirect is authenticated. CloudFront caching is disabled, and every response has `private, no-store`. The browser also returns an open page to login when its session expires.

Validation is offline, matching existing generated-license verification; there is no per-customer revocation database. Logout removes the browser's cookie. A previously copied session remains valid until expiry unless the deployment's session signing key is changed. Content already viewed or saved cannot be recalled.

The license signing **private** key is never required. A separate random session signing key is required and must be shared across all instances. Changing it invalidates existing sessions.

## AWS deployment

`deploy/aws.yaml` provisions Lambda, CloudFront, an ACM certificate, a dedicated enterprise Route 53 zone and parent NS delegation, a retained Secrets Manager session key, and logs with 14-day retention. A CAA record authorizes Amazon certificates specifically for the enterprise hostname; this avoids inheriting the public Mintlify/Vercel hostname's CAA restrictions. See [ACM CAA requirements](https://docs.aws.amazon.com/acm/latest/userguide/troubleshooting-caa.html). Lambda uses [AWS Lambda Web Adapter](https://github.com/awslabs/aws-lambda-web-adapter) in a portable container. There is no VPC, NAT gateway, database, load balancer, or provisioned concurrency. Request/compute/transfer charges are usage based; Secrets Manager and ECR storage still have ongoing charges. See [Lambda pricing](https://aws.amazon.com/lambda/pricing/) and [CloudFront pricing](https://aws.amazon.com/cloudfront/pricing/).

The dedicated zone needs only one new NS record in the parent zone, which avoids the current parent zone’s 50-record limit. It leaves the existing `docs.odigos.io` CNAME unchanged. DNS for the enterprise site, including certificate validation, lives in the child zone. This adds one hosted-zone charge; see [Route 53 pricing](https://aws.amazon.com/route53/pricing/).

Deploy in **us-east-1**, because CloudFront needs its ACM certificate there. Prerequisites: an AWS account, a public Route 53 parent zone (such as `odigos.io`) with capacity for one new NS record at `enterprise.docs.odigos.io`, Docker, and AWS CLI credentials authorized to create the resources. If DNS is hosted elsewhere, adapt certificate validation and DNS records before deploying.

From the repository root, after choosing the target account and zone:

```sh
export AWS_REGION=us-east-1
export DOCS_PARENT_HOSTED_ZONE_ID=YOUR_PUBLIC_ZONE_ID
export DOCS_IMAGE_TAG=YOUR_IMMUTABLE_RELEASE_TAG
DOCS_ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
DOCS_REGISTRY="$DOCS_ACCOUNT_ID.dkr.ecr.$AWS_REGION.amazonaws.com"

# One-time repository setup; reuse it for subsequent releases.
aws ecr create-repository --repository-name odigos-enterprise-docs \
  --image-tag-mutability IMMUTABLE --image-scanning-configuration scanOnPush=true
aws ecr get-login-password | docker login --username AWS --password-stdin "$DOCS_REGISTRY"
docker buildx build --platform linux/amd64 --provenance=false \
  -f docs/Dockerfile -t "$DOCS_REGISTRY/odigos-enterprise-docs:$DOCS_IMAGE_TAG" --push .

aws cloudformation deploy --region "$AWS_REGION" \
  --stack-name odigos-enterprise-docs --template-file docs/deploy/aws.yaml \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides \
    ImageUri="$DOCS_REGISTRY/odigos-enterprise-docs:$DOCS_IMAGE_TAG" \
    ParentHostedZoneId="$DOCS_PARENT_HOSTED_ZONE_ID"
aws cloudformation describe-stacks --stack-name odigos-enterprise-docs \
  --query 'Stacks[0].Outputs'
```

Use a new immutable tag for every update; redeploy the same stack. Retain a previous image for rollback and configure ECR lifecycle retention to bound storage costs. CloudFront does not cache private content, so releases and session expiration need no CDN invalidation. The Function URL is reachable directly, but it runs the same authentication handler and cannot bypass the login. Never publish `build/` to a separate public S3 origin or static host.

Reserved concurrency defaults to 100; size this for bursts of authenticated scripts and images, not just page navigations. A lower limit of 20 throttled asset requests during live browser testing. Adjust it after measuring load and available account quotas. Reserved concurrency does not keep instances running or add an idle compute charge. RSA verification happens only at login. Content requests validate an HMAC session locally, with no network/database call. CloudFront forwards only the docs cookie, required browser headers, and query strings. Bodies and cookies are not logged by this application; do not add request-body logging to the deployment.

For session-key rotation, replace the Secrets Manager value with at least 32 random bytes encoded as base64, then update the Lambda configuration through a stack update that changes its environment (increment `SessionKeyVersion`). Secrets Manager changes alone do not refresh an existing Lambda environment. The template deliberately retains the session secret and the enterprise hosted zone on stack deletion. For permanent teardown, record their output IDs, delete the stack (which removes only the enterprise delegation and site records), remove the ACM validation CNAME from the retained child zone, then delete that child zone and schedule the secret for deletion. Do not remove parent-zone records belonging to the public docs. Retained resources continue to incur charges until deleted.

### Automatic releases from GitHub

The Enterprise documentation workflow checks pull requests and deploys relevant changes merged into `main`. Authentication tests with the race detector, Go vet, release-script tests, CloudFormation lint, and actionlint must pass. CI builds the production image, audits compiled links/assets and source exclusions, and runs Chromium against the container on a read-only filesystem as an arbitrary non-root user. Browser tests cover private resources, invalid login, navigation, code tabs, search, mobile layout, sign-out, and session expiry using a temporary test session key.

The deployment job receives that exact verified image as a private GitHub artifact (one-day retention), publishes an immutable ECR tag, and updates the stack's `ImageUri` to the image digest. Releases are serialized, superseded docs revisions are skipped, and manual dispatch only deploys from `main`. The release script verifies Lambda's image digest and the live login/access boundary; failed verification restores the previous image.

GitHub uses OIDC to assume `odigos-enterprise-docs-github`, restricted to `repo:odigos-io/odigos-enterprise:ref:refs/heads/main`. The role is provisioned separately by `deploy/github.yaml`; PR jobs receive no AWS credentials. Permissions cover only this ECR repository, existing docs stack/function, its execution role, and its session secret. The role cannot read the license signing key, create IAM roles, or change DNS.

For a new environment, first deploy the application stack, then bootstrap the GitHub role with its output IDs (the GitHub OIDC provider must already exist):

```sh
aws cloudformation deploy --region us-east-1 \
  --stack-name odigos-enterprise-docs-github --template-file docs/deploy/github.yaml \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameter-overrides \
    DocsFunctionName=YOUR_DOCS_FUNCTION_NAME \
    DocsExecutionRoleArn=YOUR_DOCS_EXECUTION_ROLE_ARN \
    DocsSessionSecretArn=YOUR_DOCS_SESSION_SECRET_ARN
```

The current AWS account is already bootstrapped. Automatic releases use the previously deployed CloudFormation template and preserve all other parameters. Apply infrastructure/template/IAM changes explicitly with the deployment commands above; application releases do not grant the workflow permission to change those resources. The GitHub role remains in a separate bootstrap stack so the application release role cannot broaden its own permissions.

To run the same browser test locally, install Playwright 1.63.0 in a temporary directory, install its Chromium browser, and expose that directory's `node_modules` through `NODE_PATH`:

```sh
node docs/scripts/test-container.cjs YOUR_LOCAL_DOCS_IMAGE
python3 -m unittest discover -s docs/deploy -p 'test_*.py'
```

The test uses Docker, Node 22+, Python 3, and OpenSSL. The Go tests verify generated API-key compatibility separately; browser CI never reads production signing material.

### Verify the deployment before switching the public links

1. Open an enterprise deep link anonymously; it must show login. Direct assets and `/pagefind/pagefind.js` must return 401 without a cookie.
2. Sign in with a valid generated enterprise key. Verify navigation, code tabs, search, mobile layout, and the link back to OSS.
3. Use a short-lived generated key. After its `exp`, an open page returns to login and requests for documents and search assets fail. Expired and modified keys must not sign in.
4. Publish the public-site links only after the protected hostname works. Both sites retain the complete docs at this stage; move or remove pages only in the later migration PRs.

## Local authenticated preview and tests

Build the docs first. The server requires `DOCS_ORIGIN` (HTTPS origin), `DOCS_CONTENT_DIR`, and either `DOCS_SESSION_KEY_BASE64` or `DOCS_SESSION_KEY_FILE` (base64). `LISTEN_ADDR` defaults to `:8080`. For local HTTPS, supply `DOCS_TLS_CERT_FILE` and `DOCS_TLS_KEY_FILE`; otherwise terminate TLS at a trusted proxy. Do not put the session key in Git.

```sh
cd server
GOWORK=off go tool docs-server
```

The server and its tests live in the odigos repo under `odigosauth`. The tests generate their own RSA keys and exercise signature/claim rejection, generated-key compatibility, session expiry at the boundary, CSRF, cookie flags, redirect safety, and access controls on every type of content. No real customer key or AWS credentials are needed. CI also validates navigation and imports, exports every page from both editions, and validates the CloudFormation template.
