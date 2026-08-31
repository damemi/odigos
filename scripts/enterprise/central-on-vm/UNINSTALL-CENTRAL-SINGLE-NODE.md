# Uninstalling the single-node Odigos Central installation

Use `uninstall-central-single-node.sh` only on a host installed by
`install-central-single-node.sh`. Run it from any directory.

Both modes permanently delete Central, Keycloak, connector PostgreSQL, and
their data. User-supplied TLS and license files are preserved.

## 1. Remove only Central and connectors

This keeps K3s and non-root `kubectl` access:

```bash
sudo /absolute/path/uninstall-central-single-node.sh --central-only
```

## 2. Remove everything in one shot

This additionally removes the dedicated K3s service, runtime processes,
mounts, data, CNI interfaces and firewall rules, command links, non-root
kubeconfig, and installer state:

```bash
sudo /absolute/path/uninstall-central-single-node.sh --all
```

The script never modifies `/bin`, `/usr/bin`, SSH configuration, user shells,
or Tailscale. Reboot the host after full removal to clear transient loaded
kernel modules and runtime sysctl values.
