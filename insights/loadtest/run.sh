#!/usr/bin/env bash
# Start / stop the insights OTLP k6 loadtest Deployment.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
NS="${NS:-insights-loadtest}"
DEPLOY="${DEPLOY:-insights-k6}"
SCRIPT="${SCRIPT:-${ROOT}/script.js}"
VUS="${VUS:-}"
DURATION="${DURATION:-}"
SLEEP_MS="${SLEEP_MS:-}"
ENDPOINT="${ENDPOINT:-}"
TENANTS="${TENANTS:-}"

usage() {
	cat <<'EOF'
Usage: ./run.sh <start|stop|restart>

Env overrides (optional):
  NS          namespace (default: insights-loadtest)
  VUS         virtual users (patches Deployment env)
  DURATION    k6 duration (e.g. 1h, 24h)
  SLEEP_MS    pause between pushes (lower = more load)
  ENDPOINT    OTLP gRPC host:port
  TENANTS     distinct service.namespace values (scales transaction count)

Examples:
  ./run.sh start
  VUS=20 SLEEP_MS=10 ./run.sh start
  TENANTS=10 ./run.sh start
  ./run.sh stop
EOF
}

require_kubectl() {
	command -v kubectl >/dev/null 2>&1 || {
		echo "kubectl not found" >&2
		exit 1
	}
}

apply_script_configmap() {
	kubectl -n "$NS" create configmap insights-k6-script \
		--from-file=script.js="$SCRIPT" \
		--dry-run=client -o yaml | kubectl apply -f -
}

# Apply Deployment with optional env overrides in one write (no apply+set-env race).
apply_deployment() {
	VUS="$VUS" DURATION="$DURATION" SLEEP_MS="$SLEEP_MS" ENDPOINT="$ENDPOINT" TENANTS="$TENANTS" \
		python3 - "${ROOT}/k8s/deployment.yaml" <<'PY' | kubectl apply -f -
import os, sys
path = sys.argv[1]
text = open(path).read()
# Split documents; only patch the Deployment doc env values.
overrides = {
    "VUS": os.environ.get("VUS") or None,
    "DURATION": os.environ.get("DURATION") or None,
    "SLEEP_MS": os.environ.get("SLEEP_MS") or None,
    "ENDPOINT": os.environ.get("ENDPOINT") or None,
    "TENANTS": os.environ.get("TENANTS") or None,
}
overrides = {k: v for k, v in overrides.items() if v}
lines = text.splitlines(keepends=True)
out = []
i = 0
while i < len(lines):
    line = lines[i]
    out.append(line)
    stripped = line.lstrip()
    indent = line[: len(line) - len(stripped)]
    if stripped.startswith("- name: "):
        name = stripped[len("- name: ") :].strip()
        if name in overrides and i + 1 < len(lines):
            nxt = lines[i + 1]
            if nxt.lstrip().startswith("value:"):
                val_indent = nxt[: len(nxt) - len(nxt.lstrip())]
                out.append(f'{val_indent}value: "{overrides[name]}"\n')
                i += 2
                continue
    i += 1
sys.stdout.write("".join(out))
PY
}

cmd_start() {
	require_kubectl
	kubectl apply -f "${ROOT}/k8s/namespace.yaml"
	apply_script_configmap
	apply_deployment
	kubectl -n "$NS" rollout status "deploy/${DEPLOY}" --timeout=120s
	echo "loadtest running in namespace ${NS} (deploy/${DEPLOY})"
	kubectl -n "$NS" get deploy "$DEPLOY" -o jsonpath='{.spec.template.spec.containers[0].env}' | tr ',' '\n'
	echo
	echo "  stop: $0 stop"
}

cmd_stop() {
	require_kubectl
	kubectl delete -f "${ROOT}/k8s/deployment.yaml" --ignore-not-found
	echo "loadtest deployment deleted (namespace ${NS} left in place)"
}

cmd_restart() {
	cmd_stop
	cmd_start
}

main() {
	local cmd="${1:-}"
	case "$cmd" in
	start) cmd_start ;;
	stop) cmd_stop ;;
	restart) cmd_restart ;;
	-h | --help | help | "")
		usage
		[[ -n "$cmd" ]] || exit 1
		;;
	*)
		echo "unknown command: $cmd" >&2
		usage
		exit 1
		;;
	esac
}

main "$@"
