# Odigos Documentation

Source for [enterprise.docs.odigos.io](https://enterprise.docs.odigos.io), where every page (Open Source included) is behind enterprise API key sign-in. The Kubernetes Agent has two tabs: Open Source (`oss/`) and Enterprise (`enterprise/`). Odigos Central (`central/`), the VM Agent and ECS Agent (`vmagent/`), and Cloud Connectors (`cloud-connectors/`) are documented in full.

### Development

The documentation is built with [Mintlify](https://www.npmjs.com/package/mintlify). To preview changes locally:

```
# make sure you're in `/docs` folder, where `docs.json` and `package.json` files are
# install the mintlify npm package (if you don't already have it)
npm i -g mint
mint dev
```

Generated CLI, CRD, destination, instrumentation, and RBAC snippets are written under `snippets/oss`:

```
make cli-docs
make rbac-docs
cd api && make docgen
cd docs && python sync-dest-doc.py
```

`sync-dest-doc.py` updates Open Source destination pages and skips Pyroscope, which is documented only in the Enterprise tab. Enterprise pages import their own copies from `snippets/enterprise`; copy changed generated snippets there when those pages should stay in sync.

### Publishing Changes

Merging to `main` publishes through Mintlify. The Mintlify site is password-protected (Mintlify Dashboard → Access → Password), so its own hostname shows nothing without the docs password, which is never given to customers. Readers use enterprise.docs.odigos.io, a CloudFront gate in front of Mintlify: they sign in with an enterprise API key and the gate fetches pages with a shared Mintlify session. The gate is defined in `odigos-io/cloud-infra` (`aws/docs-gate`) and builds its sign-in on `odigosauth/docslogin` from this repository.

Pull requests get Mintlify preview deployments. While the site requires a password, Mintlify requires it on previews too; without it, preview URLs are public.
