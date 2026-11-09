# Odigos UI

## Development

To develop the UI, you'll need to maintain the UI kit repo: [ui-kit](https://github.com/odigos-io/ui-kit)

## Running Locally

0. The UI Kit is a private package on GitHub Packages. Give `~/.npmrc` a token that can read it, once:
    ```bash
    gh auth refresh -s read:packages
    echo "//npm.pkg.github.com/:_authToken=$(gh auth token)" >> ~/.npmrc
    ```
    Or use a classic personal access token with `read:packages` instead of `$(gh auth token)`. `yarn install` and `make build-ui` / `make push-ui` read it. Never commit an `.npmrc` that contains a token.

1. Install dependencies:
    ```bash
    yarn install
    ```

2. Create a client build:
    ```bash
    yarn build
    ```

3. Create a server build:
    ```bash
    yarn back:build
    ```

4. Start the server:
    ```bash
    yarn back:start
    ```
    You should now be able to visit the UI on [localhost:8085](http://localhost:8085).

5. Note: if you want to get real-time code updates, you'll have to run the client seperately:
    ```bash
    yarn dev
    ```
    You should now be able to visit the UI on [localhost:3000](http://localhost:3000).