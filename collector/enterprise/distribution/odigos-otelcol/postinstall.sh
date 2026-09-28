# The collector is supervised by odigos-vmagent, not by a packaged unit.
# On upgrade from a package that still shipped systemd units, drop leftover
# enablement after the unit files have been removed.
if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now odigos-otelcol-config.path 2>/dev/null || true
    systemctl disable --now odigos-otelcol-config.service 2>/dev/null || true
    systemctl disable --now odigos-otelcol-config-reload.service 2>/dev/null || true
    systemctl disable --now odigos-otelcol.service 2>/dev/null || true
    systemctl daemon-reload 2>/dev/null || true
    systemctl reset-failed odigos-otelcol.service 2>/dev/null || true
fi
