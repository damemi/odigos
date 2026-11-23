# Odigos OpenTelemetry Collector

The Odigos collector distribution, assembled by the OpenTelemetry collector builder (`ocb`) from
[`builder-config.yaml`](./builder-config.yaml). The Odigos components live next to it, under
`processors/`, `exporters/`, `extension/`, `receivers/`, `connectors/` and `providers/`, and
`builder-config.yaml` replaces each of them with its local directory.

## Building

```bash
make build            # generate + compile
make genodigoscol     # generate sources only
make build-odigoscol  # compile already-generated sources
make test             # build a collector from the registry and run every signal pipeline
```

Use the Go version in `odigosotelcol/go.mod`; if your toolchain is older, run in a container:

```bash
docker run -it -v "$(pwd)":/app -w /app golang:1.27.4-trixie make build
```

`-checklinkname=0` is not optional — `github.com/odigos-io/go-rtml` uses `//go:linkname` into
`runtime`, and the link fails without it. The Makefile passes it everywhere, including to `go test`.

`make test` constructs a real collector against
[`testdata/full-signal-config.yaml`](./odigosotelcol/testdata/full-signal-config.yaml), which has a
pipeline for every signal, so it catches a processor wired into a signal its factory does not
support — a mistake that otherwise only appears as a crash-looping gateway in a cluster.

## `odigosotelcol/` is generated, and committed

`components.go`, `main*.go`, `go.mod` and `go.sum` come from `ocb`; regenerate them rather than
editing. They are marked `linguist-generated` so GitHub collapses them in review.

Committing them (upstream does the same) means a dependency bump is a reviewable diff rather than
something that happens invisibly inside an image build, and the image can `go mod download` the
exact versions that were reviewed instead of re-resolving the module graph at build time. It is
also what makes `featuregates.go` and the smoke test possible: both must live *inside* the generated
module to be part of the binary and see the component registry. `ocb` does not delete files it did
not generate, so they survive regeneration.

`featuregates.go` enables `service.profilesSupport`, an alpha core-collector gate without which a
profiles pipeline is rejected. Gates are settable only via `--feature-gates`, i.e. process argv,
which cannot change without a restart; enabling it in code keeps adding or removing a profiles
pipeline a pure config change applied by a SIGHUP reload.

## Remote management

The normal and remotely managed modes use the same enterprise Collector image. Existing deployments
still start `/odigosotelcol`; cloud connector deployments opt in by starting the bundled
`/odigosopampsupervisor` instead. See [`managed/README.md`](./managed/README.md) for the deployment
contract, persistent state, first-boot fallback, remote configuration behavior, and security boundary.

## Metadata generation

To generate metadata for a component, create `metadata.yaml` in the component's directory and run:

```bash
make generate
```

## Troubleshooting

If `make genodigoscol` fails to resolve dependencies (typically
`invalid version: unknown revision 000000000000`), clear stale local tooling with
`git clean -dxf collector/.tools`.
