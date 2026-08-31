#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

MODE=""
STATE_DIR="/etc/odigos-central"
MARKER_FILE="${STATE_DIR}/install.env"
WORK_DIR=""
UNINSTALL_LOG=""
NAMESPACE=""
DATA_DIR=""
KUBECTL_USER=""
IMAGE_PREFIX=""
INSTALL_STATUS=""
KUBECTL=()

usage() {
  cat <<'EOF'
Uninstall the single-node Odigos Central installation.

Usage:
  sudo ./uninstall-central-single-node.sh --central-only
  sudo ./uninstall-central-single-node.sh --all

Modes (choose exactly one):
  --central-only   Remove Central, connectors, their data, host port services,
                   and bundled Central images. Keep the K3s cluster.
  --all            Remove Central and connectors, then completely remove the
                   dedicated K3s runtime, data, services, CNI state, and
                   installer-created kubectl configuration.
  -h, --help       Show this help.

The script accepts no configurable paths. It reads and validates the state
written by install-central-single-node.sh so it cannot target an arbitrary
K3s installation or data directory.
EOF
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

warn() {
  printf 'WARNING: %s\n' "$*" >&2
}

log() {
  printf '%s ==> %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"
}

cleanup() {
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
    rm -rf -- "$WORK_DIR"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

read_state_value() {
  local key="$1"
  sed -n "s/^${key}='\([^']*\)'$/\1/p" "$MARKER_FILE"
}

validate_managed_data_dir() {
  local path="$1"
  local normalized_path

  [[ "$path" =~ ^/[A-Za-z0-9._+/-]+$ ]] || return 1
  normalized_path="/${path#/}/"
  [[ "$path" != "/" && \
     "$normalized_path" != *"//"* && \
     "$normalized_path" != *"/../"* && \
     "$normalized_path" != *"/./"* ]] || return 1
  case "$path" in
    /bin | /boot | /dev | /etc | /home | /lib | /lib64 | /opt | /proc | /root | \
      /run | /sbin | /srv | /sys | /tmp | /usr | /var | /var/lib)
      return 1
      ;;
  esac
  [[ "$(awk -F/ '{ print NF-1 }' <<< "$path")" -ge 3 ]]
}

k3s_unit_exists() {
  [[ -e /etc/systemd/system/k3s.service ]] || \
    systemctl list-unit-files --no-legend k3s.service 2>/dev/null | grep -q '^k3s\.service[[:space:]]'
}

dump_k3s_diagnostics() {
  log "K3s service diagnostics"
  systemctl --no-pager --full status k3s.service 2>&1 || true
  if command -v journalctl >/dev/null 2>&1; then
    journalctl --no-pager --unit k3s.service --lines 100 2>&1 || true
  fi
}

wait_for_k3s_api() {
  local attempt
  local active_state

  if ! systemctl is-active --quiet k3s.service; then
    log "Starting K3s so Central and connector resources can be removed cleanly"
    systemctl reset-failed k3s.service >/dev/null 2>&1 || true
    systemctl start --no-block k3s.service || die "could not queue the K3s start"
  fi

  log "Waiting up to six minutes for the Kubernetes API"
  for attempt in $(seq 1 180); do
    if "${KUBECTL[@]}" get --raw=/readyz >/dev/null 2>&1; then
      return 0
    fi
    active_state="$(systemctl show k3s.service --property=ActiveState --value 2>/dev/null || true)"
    if [[ "$active_state" == "failed" ]] || \
       { (( attempt > 5 )) && [[ "$active_state" == "inactive" ]]; }; then
      dump_k3s_diagnostics
      die "K3s stopped before its API became ready"
    fi
    sleep 2
  done

  dump_k3s_diagnostics
  die "Kubernetes API did not become ready within six minutes"
}

remove_host_port_services() {
  local unit
  local units=(
    odigos-central-ui-port.service
    odigos-central-backend-port.service
    odigos-central-keycloak-port.service
  )

  log "Removing Central host port services"
  systemctl disable --now "${units[@]}" 2>/dev/null || true
  for unit in "${units[@]}"; do
    if [[ -e "/etc/systemd/system/${unit}" ]]; then
      log "  removing /etc/systemd/system/${unit}"
      rm -f -- "/etc/systemd/system/${unit}"
    fi
    rm -f -- "/etc/systemd/system/multi-user.target.wants/${unit}"
  done
  systemctl daemon-reload
}

remove_recorded_images() {
  local image
  local images_file="${STATE_DIR}/images.txt"
  local -a images=()

  [[ -x /usr/local/bin/k3s ]] || return 0
  systemctl is-active --quiet k3s.service || return 0

  if [[ -s "$images_file" ]]; then
    while IFS= read -r image; do
      [[ -n "$image" ]] && images+=("$image")
    done < "$images_file"
  else
    while IFS= read -r image; do
      case "$image" in
        "${IMAGE_PREFIX}"/odigos-enterprise-central-* | \
          "${IMAGE_PREFIX}"/odigos-enterprise-connector-*)
          images+=("$image")
          ;;
      esac
    done < <(/usr/local/bin/k3s ctr --namespace k8s.io images list --quiet 2>/dev/null || true)
  fi

  if (( ${#images[@]} > 0 )); then
    log "Removing ${#images[@]} recorded Central image references from containerd"
    for image in "${images[@]}"; do
      log "  removing image reference: ${image}"
      /usr/local/bin/k3s ctr --namespace k8s.io images remove "$image" >/dev/null 2>&1 || \
        warn "could not remove image reference (it may already be absent): ${image}"
    done
  fi
}

remove_central() {
  remove_host_port_services
  wait_for_k3s_api

  if "${KUBECTL[@]}" get namespace "$NAMESPACE" >/dev/null 2>&1 && \
     "${KUBECTL[@]}" get crd odigoscloudconnectors.odigos.io >/dev/null 2>&1; then
    log "Deleting connector resources and waiting for managed-resource cleanup"
    timeout --foreground 21m "${KUBECTL[@]}" --namespace "$NAMESPACE" \
      delete odigoscloudconnectors --all --ignore-not-found --wait=true --timeout=20m
  fi

  log "Deleting the ${NAMESPACE} namespace and all Central persistent data"
  timeout --foreground 21m "${KUBECTL[@]}" delete namespace "$NAMESPACE" \
    --ignore-not-found --wait=true --timeout=20m

  log "Deleting the Odigos cloud connector CRD"
  "${KUBECTL[@]}" delete crd odigoscloudconnectors.odigos.io --ignore-not-found

  remove_recorded_images

  log "Removing staged Central image archives and generated Helm values"
  rm -f -- \
    "${DATA_DIR}/agent/images/odigos-central-images.tar" \
    "${DATA_DIR}"/agent/images/odigos-central-image-*.oci.tar \
    "${DATA_DIR}"/agent/images/odigos-central-image-*.oci.tar.odigos-new \
    "${STATE_DIR}/values.yaml" \
    "${STATE_DIR}/images.txt"
  sed -i "s/^STATUS='[^']*'$/STATUS='central-removed'/" "$MARKER_FILE"
  INSTALL_STATUS="central-removed"
}

collect_process_tree() {
  local parent_pid="$1"
  local child_pid

  RUNTIME_PIDS+=("$parent_pid")
  while IFS= read -r child_pid; do
    [[ -n "$child_pid" ]] && collect_process_tree "$child_pid"
  done < <(ps -e -o ppid= -o pid= | awk -v wanted="$parent_pid" '$1 == wanted { print $2 }')
}

collect_k3s_runtime_pids() {
  local exe
  local pid
  local proc_exe
  local -A seen=()
  local -a roots=()

  RUNTIME_PIDS=()
  for proc_exe in /proc/[0-9]*/exe; do
    [[ -e "$proc_exe" ]] || continue
    exe="$(readlink "$proc_exe" 2>/dev/null || true)"
    case "$exe" in
      "${DATA_DIR}"/data/*/bin/containerd-shim* | \
        "${DATA_DIR}"/data/*/bin/containerd)
        pid="${proc_exe#/proc/}"
        pid="${pid%/exe}"
        roots+=("$pid")
        ;;
    esac
  done

  for pid in "${roots[@]}"; do
    collect_process_tree "$pid"
  done

  for pid in "${RUNTIME_PIDS[@]}"; do
    if [[ -z "${seen[$pid]:-}" ]]; then
      seen[$pid]=1
      printf '%s\n' "$pid"
    fi
  done
}

stop_k3s_runtime() {
  local attempt
  local any_alive
  local main_pid
  local pid
  local -a runtime_pids=()

  log "Disabling and stopping K3s"
  systemctl disable k3s.service >/dev/null 2>&1 || true
  systemctl stop --no-block k3s.service >/dev/null 2>&1 || true
  for attempt in $(seq 1 60); do
    main_pid="$(systemctl show k3s.service --property=MainPID --value 2>/dev/null || true)"
    [[ "$main_pid" =~ ^[0-9]+$ && "$main_pid" -gt 1 && -d "/proc/${main_pid}" ]] || break
    sleep 1
  done
  main_pid="$(systemctl show k3s.service --property=MainPID --value 2>/dev/null || true)"
  if [[ "$main_pid" =~ ^[0-9]+$ && "$main_pid" -gt 1 && -d "/proc/${main_pid}" ]]; then
    warn "K3s did not stop within 60 seconds; terminating its main process"
    kill -TERM "$main_pid" 2>/dev/null || true
    sleep 5
    [[ -d "/proc/${main_pid}" ]] && kill -KILL "$main_pid" 2>/dev/null || true
  fi
  systemctl is-active --quiet k3s.service && die "K3s is still active; runtime files were not removed"

  while IFS= read -r pid; do
    [[ -n "$pid" ]] && runtime_pids+=("$pid")
  done < <(collect_k3s_runtime_pids)

  if (( ${#runtime_pids[@]} > 0 )); then
    log "Stopping ${#runtime_pids[@]} remaining K3s container runtime processes"
    kill -TERM "${runtime_pids[@]}" 2>/dev/null || true
    for attempt in $(seq 1 30); do
      any_alive=false
      for pid in "${runtime_pids[@]}"; do
        if [[ -d "/proc/${pid}" ]]; then
          any_alive=true
          break
        fi
      done
      $any_alive || break
      sleep 1
    done
    for pid in "${runtime_pids[@]}"; do
      if [[ -d "/proc/${pid}" ]]; then
        kill -KILL "$pid" 2>/dev/null || true
      fi
    done
    sleep 1
  fi
}

managed_mounts() {
  findmnt --raw --noheadings --output TARGET | awk -v data="$DATA_DIR" '
    function below(path, root) {
      return index(path, root "/") == 1
    }
    below($0, data) ||
    below($0, "/run/k3s") ||
    below($0, "/var/lib/kubelet/pods") ||
    below($0, "/var/lib/kubelet/plugins") ||
    index($0, "/run/netns/cni-") == 1 {
      print length($0) "\t" $0
    }
  ' | sort --numeric-sort --reverse | cut -f2-
}

unmount_k3s_paths() {
  local mount_path
  local -a mounts=()

  while IFS= read -r mount_path; do
    [[ -n "$mount_path" ]] && mounts+=("$mount_path")
  done < <(managed_mounts)

  if (( ${#mounts[@]} > 0 )); then
    log "Unmounting ${#mounts[@]} K3s-managed mount points"
  fi
  for mount_path in "${mounts[@]}"; do
    log "  unmounting ${mount_path}"
    if ! umount "$mount_path" 2>/dev/null; then
      warn "normal unmount was busy; lazily detaching the K3s mount: ${mount_path}"
      umount --lazy "$mount_path" || die "could not unmount K3s path: ${mount_path}"
    fi
  done

  if managed_mounts | grep -q .; then
    managed_mounts >&2
    die "K3s mounts remain; refusing to delete their backing directories"
  fi
}

remove_k3s_network_state() {
  local embedded_firewall_command
  local embedded_firewall_dir
  local interface_name
  local namespace_name
  local save_command
  local restore_command
  local family
  local rules_file
  local filtered_rules_file
  local interfaces=(cni0 flannel.1 flannel-v6.1 kube-ipvs0 flannel-wg flannel-wg-v6)

  log "Removing K3s CNI network namespaces"
  if command -v ip >/dev/null 2>&1; then
    while IFS= read -r namespace_name; do
      [[ "$namespace_name" == cni-* ]] || continue
      log "  deleting network namespace ${namespace_name}"
      ip netns delete "$namespace_name" 2>/dev/null || die "could not delete network namespace: ${namespace_name}"
    done < <(ip netns list 2>/dev/null | awk '{ print $1 }')

    log "Removing K3s CNI interfaces"
    for interface_name in "${interfaces[@]}"; do
      if ip link show "$interface_name" >/dev/null 2>&1; then
        log "  deleting interface ${interface_name}"
        ip link delete "$interface_name" 2>/dev/null || die "could not delete interface: ${interface_name}"
      fi
    done
  fi

  embedded_firewall_command="$(find "${DATA_DIR}/data" -path '*/bin/iptables-save' -print -quit 2>/dev/null || true)"
  if [[ -n "$embedded_firewall_command" ]]; then
    embedded_firewall_dir="$(dirname "$embedded_firewall_command")"
    export PATH="${PATH}:${embedded_firewall_dir}:${embedded_firewall_dir}/aux"
    log "K3s-embedded firewall tools are available at ${embedded_firewall_dir}"
  fi

  for family in ipv4 ipv6; do
    if [[ "$family" == "ipv4" ]]; then
      save_command="iptables-save"
      restore_command="iptables-restore"
    else
      save_command="ip6tables-save"
      restore_command="ip6tables-restore"
    fi
    command -v "$save_command" >/dev/null 2>&1 || \
      die "cannot verify removal of K3s ${family} firewall rules: ${save_command} is unavailable"
    command -v "$restore_command" >/dev/null 2>&1 || \
      die "cannot remove K3s ${family} firewall rules: ${restore_command} is unavailable"

    rules_file="${WORK_DIR}/${family}-rules"
    filtered_rules_file="${WORK_DIR}/${family}-rules-without-k3s"
    "$save_command" > "$rules_file" 2>/dev/null || {
      die "could not read ${family} firewall rules"
    }
    awk '
      index(toupper($0), "KUBE-") == 0 &&
      index(toupper($0), "CNI-") == 0 &&
      index(tolower($0), "flannel") == 0 { print }
    ' "$rules_file" > "$filtered_rules_file"
    if ! cmp -s "$rules_file" "$filtered_rules_file"; then
      log "Removing K3s-only ${family} firewall chains and references"
      if ! "$restore_command" --test < "$filtered_rules_file"; then
        die "filtered ${family} firewall rules failed validation; active rules were not changed"
      elif ! "$restore_command" < "$filtered_rules_file"; then
        die "could not apply the validated ${family} firewall cleanup"
      fi
    fi
  done
}

remove_file_if_present() {
  local path="$1"
  if [[ -e "$path" || -L "$path" ]]; then
    log "  removing ${path}"
    rm -f -- "$path"
  fi
}

remove_tree_if_present() {
  local path="$1"
  if [[ -e "$path" || -L "$path" ]]; then
    log "  removing ${path}"
    rm -rf --one-file-system -- "$path"
  fi
}

remove_user_kubeconfig() {
  local default_kubeconfig
  local user_home

  [[ -n "$KUBECTL_USER" ]] || return 0
  user_home="$(getent passwd "$KUBECTL_USER" 2>/dev/null | cut -d: -f6 || true)"
  [[ -n "$user_home" && "$user_home" == /* ]] || return 0

  default_kubeconfig="${user_home}/.kube/config"
  if [[ -L "$default_kubeconfig" ]] && \
     [[ "$(readlink "$default_kubeconfig")" == "odigos-central.yaml" ]]; then
    remove_file_if_present "$default_kubeconfig"
  fi
  remove_file_if_present "${user_home}/.kube/odigos-central.yaml"
  rmdir "${user_home}/.kube" 2>/dev/null || true
}

remove_k3s_files() {
  local command_name
  local command_path
  local link_target
  local path

  log "Removing installer-created K3s command links"
  for command_name in kubectl crictl ctr; do
    command_path="/usr/local/bin/${command_name}"
    if [[ -L "$command_path" ]]; then
      link_target="$(readlink "$command_path")"
      if [[ "$link_target" == "k3s" || "$link_target" == "/usr/local/bin/k3s" ]]; then
        remove_file_if_present "$command_path"
      fi
    fi
  done

  log "Removing K3s services and executables"
  for path in \
    /etc/systemd/system/k3s.service \
    /etc/systemd/system/k3s.service.env \
    /etc/systemd/system/multi-user.target.wants/k3s.service \
    /usr/local/bin/k3s \
    /usr/local/bin/.k3s.odigos-new \
    /usr/local/bin/k3s-killall.sh \
    /usr/local/bin/k3s-uninstall.sh; do
    remove_file_if_present "$path"
  done
  systemctl reset-failed k3s.service >/dev/null 2>&1 || true
  systemctl daemon-reload

  log "Removing K3s runtime, data, CNI, configuration, and logs"
  remove_tree_if_present "$DATA_DIR"
  for path in \
    /var/lib/kubelet \
    /var/lib/cni \
    /etc/cni/net.d \
    /etc/rancher/k3s \
    /etc/rancher/node \
    /run/k3s \
    /run/flannel; do
    [[ "$path" == "$DATA_DIR" ]] || remove_tree_if_present "$path"
  done
  find /run/netns -mindepth 1 -maxdepth 1 -name 'cni-*' -delete 2>/dev/null || true

  remove_file_if_present /etc/modules-load.d/k3s.conf
  remove_file_if_present /etc/sysctl.d/90-kubelet.conf
}

remove_installer_state() {
  log "Removing Odigos Central installer state"
  remove_file_if_present "${STATE_DIR}/images.txt"
  remove_file_if_present "${STATE_DIR}/values.yaml"
  remove_file_if_present "$MARKER_FILE"
  rmdir "$STATE_DIR" 2>/dev/null || \
    log "Preserving ${STATE_DIR} because it contains user-supplied files"
  remove_tree_if_present /var/log/odigos-central
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --central-only)
      [[ -z "$MODE" ]] || die "choose exactly one uninstall mode"
      MODE="central-only"
      shift
      ;;
    --all)
      [[ -z "$MODE" ]] || die "choose exactly one uninstall mode"
      MODE="all"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n "$MODE" ]] || die "choose --central-only or --all"
[[ "$EUID" -eq 0 ]] || die "run this uninstaller as root"
[[ "$(uname -s)" == "Linux" ]] || die "this uninstaller supports Linux hosts only"

for command_name in awk chmod cmp cut date dirname find findmnt getent grep ip kill mkdir mktemp ps \
  readlink rm sed seq sleep sort stat systemctl tee timeout touch umount uname; do
  require_command "$command_name"
done

[[ -f "$MARKER_FILE" && ! -L "$MARKER_FILE" ]] || \
  die "managed installation state is missing: ${MARKER_FILE}"
[[ "$(stat -c %u "$MARKER_FILE")" == "0" ]] || die "installation state is not owned by root"

INSTALL_STATUS="$(read_state_value STATUS)"
NAMESPACE="$(read_state_value NAMESPACE)"
DATA_DIR="$(read_state_value DATA_DIR)"
KUBECTL_USER="$(read_state_value KUBECTL_USER)"
IMAGE_PREFIX="$(read_state_value IMAGE_PREFIX)"
IMAGE_PREFIX="${IMAGE_PREFIX:-registry.odigos.io}"

[[ "$INSTALL_STATUS" == "ready" || "$INSTALL_STATUS" == "installing" || \
   "$INSTALL_STATUS" == "central-removed" || "$INSTALL_STATUS" == "cluster-removing" ]] || \
  die "unrecognized managed installation status"
[[ "$NAMESPACE" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && ${#NAMESPACE} -le 63 ]] || \
  die "invalid namespace in installation state"
validate_managed_data_dir "$DATA_DIR" || die "unsafe K3s data directory in installation state: ${DATA_DIR}"
[[ "$IMAGE_PREFIX" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?(/[A-Za-z0-9._/-]+)?$ ]] || \
  die "invalid image prefix in installation state"
[[ -z "$KUBECTL_USER" || "$KUBECTL_USER" =~ ^[A-Za-z_][A-Za-z0-9_.-]*[$]?$ ]] || \
  die "invalid kubectl user in installation state"
if [[ "$INSTALL_STATUS" == "cluster-removing" && "$MODE" != "all" ]]; then
  die "a full cluster removal is already in progress; rerun with --all"
fi
if [[ "$INSTALL_STATUS" == "ready" || "$INSTALL_STATUS" == "installing" ]]; then
  k3s_unit_exists || die "the managed K3s service is missing; connector cleanup cannot be verified"
  [[ -x /usr/local/bin/k3s ]] || die "the managed K3s executable is missing; connector cleanup cannot be verified"
elif [[ "$MODE" == "central-only" ]]; then
  k3s_unit_exists || die "K3s is no longer installed"
fi

mkdir -p /var/log/odigos-central
chmod 0700 /var/log/odigos-central
UNINSTALL_LOG="/var/log/odigos-central/uninstall-$(date -u '+%Y%m%dT%H%M%SZ').log"
touch "$UNINSTALL_LOG"
chmod 0600 "$UNINSTALL_LOG"
exec > >(tee -a "$UNINSTALL_LOG") 2>&1

WORK_DIR="$(mktemp -d /tmp/odigos-central-uninstall.XXXXXX)"
KUBECTL=(/usr/local/bin/k3s kubectl --kubeconfig /etc/rancher/k3s/k3s.yaml --request-timeout=30s)

log "Starting Odigos Central single-node uninstallation"
log "Mode: ${MODE}"
log "Installation state: ${MARKER_FILE}"
log "K3s data directory: ${DATA_DIR}"
log "Uninstallation transcript: ${UNINSTALL_LOG}"

if [[ "$INSTALL_STATUS" == "ready" || "$INSTALL_STATUS" == "installing" ]]; then
  remove_central
elif [[ "$INSTALL_STATUS" == "cluster-removing" ]]; then
  log "Resuming an interrupted full cluster removal"
else
  log "Central and connectors were already removed"
  remove_host_port_services
fi

if [[ "$MODE" == "central-only" ]]; then
  log "Uninstallation complete: Central and connectors were removed; K3s remains installed"
  exit 0
fi

sed -i "s/^STATUS='[^']*'$/STATUS='cluster-removing'/" "$MARKER_FILE"
INSTALL_STATUS="cluster-removing"
stop_k3s_runtime
unmount_k3s_paths
remove_k3s_network_state
remove_user_kubeconfig
remove_k3s_files
remove_installer_state

log "Uninstallation complete: Central, connectors, and the dedicated K3s cluster were removed"
log "User-supplied TLS certificate, TLS key, and license files were preserved"
log "Reboot the host before reusing it to clear transient kernel and network state"
