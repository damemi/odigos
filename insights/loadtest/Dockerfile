# Build k6 with grafana/xk6-client-tracing (OTLP multi-service traces).
# Binary installed as `k6` so both Deployment and k6-operator work.
#
# Upstream TemplatedGenerator randomizes identity that Insights learns as
# baseline facts, so a synthetic run mints new findings forever instead of
# converging:
#   - instrumentation scope per resource (k6-scope-name/<rand>) -> D5 libraries
#   - net.peer.port / host port per span (random.Port()) -> D2 egress, which
#     keys on {host, port}, so every trace reaches a "new" peer
# We vendor+patch those to fixed values before the xk6 build. Deliberate
# anomalies belong in script.js, not in generator randomness.
#
# Image: public.ecr.aws/e1u7m2k3/insights-k6-runner
ARG GO_VERSION=1.25.4
ARG XK6_CLIENT_TRACING_VERSION=v0.0.10

FROM golang:${GO_VERSION}-alpine AS build
ARG XK6_CLIENT_TRACING_VERSION
RUN apk add --no-cache git build-base
ENV GOTOOLCHAIN=auto
RUN go install go.k6.io/xk6/cmd/xk6@latest

WORKDIR /ext
RUN git clone --depth 1 --branch "${XK6_CLIENT_TRACING_VERSION}" \
      https://github.com/grafana/xk6-client-tracing.git .
# Pin scope identity so D5 (libraries) does not learn a new lib every trace,
# and pin peer ports so D2 (egress) converges on a fixed peer set.
RUN sed -i \
      -e 's|scopeSpans.Scope().SetName("k6-scope-name/" + random.String(15))|scopeSpans.Scope().SetName("go.opentelemetry.io/otel")|' \
      -e 's|scopeSpans.Scope().SetVersion("k6-scope-version:v" + strconv.Itoa(random.IntBetween(0, 99)) + "." + strconv.Itoa(random.IntBetween(0, 99)))|scopeSpans.Scope().SetVersion("1.28.0")|' \
      -e '/"strconv"/d' \
      -e 's|random.Port()|8080|g' \
      pkg/tracegen/templated.go

# A sed that no longer matches is silent, and the only symptom is a findings
# list flooded weeks later. Fail the build instead.
RUN for pat in 'k6-scope-name/' 'k6-scope-version:v' 'random.Port()'; do \
      if grep -qF "$pat" pkg/tracegen/templated.go; then \
        echo "patch did not apply, upstream moved: $pat" >&2; exit 1; \
      fi; \
    done

WORKDIR /src
RUN mkdir -p /out && \
    CGO_ENABLED=0 xk6 build \
      --with github.com/grafana/xk6-client-tracing=/ext \
      --output /out/k6

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 12345 k6
COPY --from=build /out/k6 /usr/bin/k6
USER 12345
WORKDIR /home/k6
ENTRYPOINT ["k6"]
