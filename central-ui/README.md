# Odigos Centralized UI

## Development

To develop the UI, you'll need to maintain the UI kit repo: [ui-kit](https://github.com/odigos-io/ui-kit)

## Running Locally

1. Run Redis:

   ```bash
   docker pull redis:latest
   docker run --name redis-local -p 6379:6379 -d redis:latest
   ```

2. Install dependencies. `@odigos/ui-kit` is a private package on GitHub Packages, so set up a token once (your GitHub account needs read access to the package):

   ```bash
   gh auth refresh -s read:packages
   echo "//npm.pkg.github.com/:_authToken=$(gh auth token)" >> ~/.npmrc
   echo "@odigos:registry=https://npm.pkg.github.com" >> ~/.npmrc  # yarn v1 needs the alias scope to authenticate the tarball
   yarn install
   ```

   `central-ui/.npmrc` maps the `@odigos-io` scope to GitHub Packages without a token. `make build-central-ui` forwards `~/.npmrc` to the image build as a BuildKit secret (`id=npmrc`); it is never stored in an image layer.

3. Start the OSS backend:

   ```bash
   cd ${ODIGOS_OSS_DIR}/frontend/webapp
   yarn
   yarn build
   yarn back:build
   yarn back:start
   ```

4. Start the Central backend:

   ```bash
   cd ./central-backend
   go run .
   ```

5. Start the Central proxy:

   ```bash
   cd ./central-proxy
   go run .
   ```

6. Start the Central UI:

   ```bash
   cd ./central-ui
   yarn
   yarn dev
   ```
