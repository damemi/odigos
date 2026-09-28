getent passwd odigos >/dev/null || useradd --system --user-group --no-create-home --shell /sbin/nologin odigos

# Older packages shipped systemd units. Stop/disable them here (before unpack
# removes the unit files) so an upgrade does not leave a running collector or
# fail the install when the units are already gone.
if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now odigos-otelcol-config.path 2>/dev/null || true
    systemctl disable --now odigos-otelcol-config.service 2>/dev/null || true
    systemctl disable --now odigos-otelcol-config-reload.service 2>/dev/null || true
    systemctl disable --now odigos-otelcol.service 2>/dev/null || true
fi
