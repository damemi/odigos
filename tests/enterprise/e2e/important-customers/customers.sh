# =============================================================================
# Customer configuration registry
# =============================================================================
# To add a new customer:
#   1. Add their slug to the CUSTOMERS array (use underscores, no hyphens)
#   2. Declare a <slug>_helm_sets bash array with their helm --set flags
#   3. Add a matching include entry in .github/workflows/build.yaml
#
# helm-install.sh only sets image.tag and onPremToken (from env).
# Everything else — modes, crons, feature flags — is owned here per customer.
#
# Optional per-customer variables:
#   <slug>_instrumented_assert_file=<path>  →  assert file for the "simple-demo instrumented" step.
#                                              Path is relative to the test directory.
#   <slug>_odigos_installed_assert_file=<filename>  →  filename under tests/common/assert in the OSS repo
#                                                       for the "Verify Odigos Installation" step.
#                                                       (e.g. odigos-installed.yaml, odigos-installed-no-device-plugin.yaml)
#   <slug>_warmup_java_lazy_client_probes=true  →  run buybot once before measured traffic.
#                                                   Needed for java-ebpf (probes load lazily on first
#                                                   class transform). Skip for customers that already
#                                                   produce a complete first-request trace (e.g. kratos).
#
# Array format: each --set and its key=value MUST be separate elements:
#   "--set" "key=value"   ✓
#   "--set key=value"     ✗  (cobra parses "set key" as the flag name → unknown flag)
#
# If multiple customers share the same helm configuration, use a single
# descriptive slug that represents the shared setup (e.g. "java_ebpf_direct")
# rather than duplicating entries. The slug doesn't have to be a customer name —
# it can describe the configuration "profile" being tested.
# =============================================================================

CUSTOMERS=(
  "greatwall"
  "kratos"
)

# ---------------------------------------------------------------------------
# greatwall
# Description: Greatwall - init-container, java-ebpf-instrumentations, legacy-dotnet-instrumentation, odiglet-extended
# ---------------------------------------------------------------------------
greatwall_odigos_installed_assert_file="odigos-installed-no-device-plugin.yaml"
greatwall_instrumented_assert_file="../../common/assert/demo-instrumented-java-ebpf-no-restart-dotnet-legacy.yaml"
greatwall_warmup_java_lazy_client_probes=true
declare -a greatwall_helm_sets=(
  "--set" "instrumentor.mountMethod=k8s-init-container"
  # This includes the greatwall profile, which includes the java-ebpf-instrumentations and legacy-dotnet-instrumentation
  "--set" "profiles={greatwall}"
  "--set" "odiglet.extended=true"
  # Reduce collector CPU requests to fit the kind node.
  # We keep size_m (default) so gateway replica counts stay at 2 and match the assert files.
  "--set" "collectorGateway.requestCPUm=100"
  "--set" "collectorNode.requestCPUm=50"
)
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# kratos
# Description: kratos profile, virtual device [default mount method]
# ---------------------------------------------------------------------------
kratos_odigos_installed_assert_file="odigos-installed.yaml"
kratos_instrumented_assert_file="../../common/assert/instrumented-and-pipeline.yaml"
declare -a kratos_helm_sets=(
  "--set" "profiles={kratos}"
)
# ---------------------------------------------------------------------------
