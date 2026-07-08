# Bundled Lambda layer artifacts

The AWS connector publishes these OpenTelemetry Lambda layers into customer accounts when it instruments
a Lambda and no suitable layer already exists in the account.

## Layout

The connector reads artifacts at `/var/odigos/lambda/<language>/<arch>/layer.zip` (see
`instrumentor.DefaultLayerDir`). Stage them here with the matching structure:

```
layers/
  python/x86_64/layer.zip
  python/arm64/layer.zip
  nodejs/x86_64/layer.zip
  nodejs/arm64/layer.zip
  java/x86_64/layer.zip
  java/arm64/layer.zip
```

- `language` is the OTel layer language key (`python`, `nodejs`, `java`, `dotnet`) — see
  `instrumentor.LanguageFromRuntime`.
- `arch` is the Lambda architecture (`x86_64`, `arm64`).
- `layer.zip` is a valid Lambda layer zip (the OTel SDK/auto-instrumentation wrapper + collector
  extension), e.g. an `open-telemetry/opentelemetry-lambda` release asset, or an Odigos-built layer.

## Providing artifacts

Either commit the zips here, or pass `*_LAYER_URL` build ARGs to the connector
[`Dockerfile`](../Dockerfile) to fetch them at build time. Anything not provided is simply absent;
the connector falls back to discovering an in-account layer and logs per function when none is found.
