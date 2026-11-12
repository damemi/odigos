if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now odigos-otelcol-config.path 2>/dev/null || true
    systemctl disable --now odigos-otelcol-config.service 2>/dev/null || true
    systemctl disable --now odigos-otelcol-config-reload.service 2>/dev/null || true
    systemctl disable --now odigos-otelcol.service 2>/dev/null || true
    systemctl daemon-reload 2>/dev/null || true
fi
