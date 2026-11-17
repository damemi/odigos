#!/bin/bash

# Waits until every instrumented workload in a namespace runs the pods Odigos wants for it.
# Waits for the outcome to settle only - tests assert the outcome itself (enabled, healthy, ...).
# Waits until the chainsaw step times out, then prints the instrumentation state.
#
# Usage: wait_for_instrumentation.sh [namespace]

set -euo pipefail

namespace="${1:-default}"

# Label set on pods by the Odigos pods webhook, with the agents config the pod was created with.
AGENTS_HASH_LABEL="odigos.io/agents-meta-hash"

# Prints why the pods of one workload are not settled yet, nothing when they are.
# Args: InstrumentationConfig json, pods json, InstrumentationInstances json.
check_pods() {
  jq -r --argjson ic "$1" --argjson instances "$3" --arg label "$AGENTS_HASH_LABEL" '
    def ready: any(.status.conditions[]?; .type == "Ready" and .status == "True");
    def agents_hash: .metadata.labels[$label] // "";
    def has_reported_instance:
      .metadata.name as $pod
      | any($instances.items[]; .metadata.labels.ownerPodName == $pod and .status.healthy != null);

    [.items[] | select(.status.phase != "Succeeded" and .status.phase != "Failed")] as $pods
    | if ($pods | length) == 0 then "no running pods"
      else $pods[] | "pod \(.metadata.name): " + (
        if .metadata.deletionTimestamp then "terminating"
        elif (ready | not) then "not ready"
        # agent disabled: pods must not run agents.
        elif ($ic.spec.agentInjectionEnabled // false) | not then
          if agents_hash != "" then "runs agents that are disabled" else empty end
        # distros with no restart: agents attach to running pods and report an InstrumentationInstance.
        elif $ic.spec.podManifestInjectionOptional // false then
          if has_reported_instance then empty else "no InstrumentationInstance reported yet" end
        # distros with a restart: pods must be created with the current agents config.
        elif agents_hash != $ic.spec.agentsMetaHash then
          "agents hash \"\(agents_hash)\", waiting for \"\($ic.spec.agentsMetaHash)\""
        else empty end)
      end' <<<"$2"
}

# Prints why one workload is not settled yet, nothing when it is.
# Args: InstrumentationConfig json, InstrumentationInstances json.
check_workload() {
  local ic="$1" instances="$2"

  # Unknown while Odigos still waits for runtime detection or the node collector.
  local agent_enabled
  agent_enabled=$(jq -r '(.status.conditions[]? | select(.type == "AgentEnabled") | .status) // "missing"' <<<"$ic")
  if [ "$agent_enabled" != "True" ] && [ "$agent_enabled" != "False" ]; then
    echo "agent enabled decision pending (AgentEnabled: $agent_enabled)"
    return
  fi

  # The InstrumentationConfig is owned by its workload.
  local kind group name
  read -r kind group name < <(jq -r '.metadata.ownerReferences[0] // {} | "\(.kind) \((.apiVersion // "") | split("/")[0]) \(.name)"' <<<"$ic")
  case "$kind" in
  Deployment | StatefulSet | DaemonSet | Rollout) ;;
  # Odigos does not restart jobs, their next run picks up the agents.
  Job | CronJob) return ;;
  *)
    echo "unsupported owner workload kind: $kind"
    return
    ;;
  esac

  local selector pods
  selector=$(kubectl get "$kind.$group/$name" -n "$namespace" -o json |
    jq -r '.spec.selector.matchLabels // {} | to_entries | map("\(.key)=\(.value)") | join(",")') || selector=""
  if [ -z "$selector" ]; then
    echo "failed to get the pod selector of $kind/$name"
    return
  fi
  pods=$(kubectl get pods -n "$namespace" -l "$selector" -o json) || {
    echo "failed to list the pods of $kind/$name"
    return
  }
  check_pods "$ic" "$pods" "$instances" || echo "failed to check the pods of $kind/$name"
}

# Prints one line per workload that is not settled yet, nothing when all are.
# A failed query is printed as pending, so it never passes the wait.
check_namespace() {
  local ics instances
  ics=$(kubectl get instrumentationconfigs -n "$namespace" -o json) || {
    echo "failed to list InstrumentationConfigs"
    return
  }
  instances=$(kubectl get instrumentationinstances -n "$namespace" -o json) || {
    echo "failed to list InstrumentationInstances"
    return
  }
  if [ "$(jq '.items | length' <<<"$ics")" -eq 0 ]; then
    echo "no InstrumentationConfigs yet"
    return
  fi

  local ic
  while read -r ic; do
    check_workload "$ic" "$instances" | sed "s|^|$(jq -r '.metadata.name' <<<"$ic"): |"
  done < <(jq -c '.items[]' <<<"$ics")
}

print_diagnostics() {
  kubectl get instrumentationconfigs -n "$namespace" --request-timeout=10s \
    -o custom-columns='NAME:.metadata.name,AGENT_ENABLED:.status.conditions[?(@.type=="AgentEnabled")].reason,ROLLOUT:.status.conditions[?(@.type=="WorkloadRollout")].reason,PODS_MANIFEST_INJECTION:.status.conditions[?(@.type=="PodsManifestInjection")].reason' || true
  kubectl get pods -n "$namespace" --request-timeout=10s -o wide -L "$AGENTS_HASH_LABEL" || true
}

source "$(dirname "$0")/wait_until.sh"

last_pending=""
check_instrumentation() {
  local pending
  pending=$(check_namespace) || pending+=$'\n'"failed to check the namespace"
  if [ -z "$pending" ]; then
    echo "instrumentation settled in namespace $namespace"
    return 0
  fi
  # Print only changes, to keep the log readable.
  if [ "$pending" != "$last_pending" ]; then
    echo "[$(date +%T)] waiting for:"
    echo "$pending"
    last_pending="$pending"
  fi
  return 1
}

wait_until check_instrumentation print_diagnostics
