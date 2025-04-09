# Odigos Centralized UI

## Development

To develop the UI, you'll need to maintain the UI kit repo: [ui-kit](https://github.com/odigos-io/ui-kit)

## Running Locally

1. Run Redis:

   ```bash
   docker pull redis:latest
   docker run --name redis-local -p 6379:6379 -d redis:latest
   ```

2. Install dependencies:

   ```bash
   yarn install
   ```

3. Start the OSS backend:

   ```bash
   cd ${ODIGOS_OSS_DIR}/frontend/webapp
   yarn
   yarn build
   yarn back:build
   yarn back:start
   ```

4. Start the Centralized backend:

   ```bash
   cd ./centralized-backend
   go run .
   ```

5. Start the Centralized proxy:

   ```bash
   cd ./centralized-proxy
   go run .
   ```

6. Start the Centralized UI:

   ```bash
   cd ./centralized-ui
   yarn
   yarn dev
   ```
