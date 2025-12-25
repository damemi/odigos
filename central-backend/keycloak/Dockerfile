FROM quay.io/keycloak/keycloak:24.0.3

# Build with health and metrics endpoints enabled
RUN /opt/keycloak/bin/kc.sh build --health-enabled=true --metrics-enabled=true

# Use the default Keycloak entrypoint
ENTRYPOINT ["/opt/keycloak/bin/kc.sh"]
