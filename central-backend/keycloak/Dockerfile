FROM quay.io/keycloak/keycloak:24.0.3

# CVE-2026-91403: stop trusting the compromised eMudhra/emSign roots (and the Sectigo
# "AAA Certificate Services" cross-sign root, if present). Keycloak 24 gets no upstream
# image respin with the fixed ca-certificates, so remove them from the Java trust store here.
# Adapted from keycloak/keycloak#54308. The build fails if any matching entry remains.
USER root
RUN KS=/etc/pki/ca-trust/extracted/java/cacerts && \
    distrusted() { \
      listing="$(keytool -list -v -keystore "${KS}" -storepass changeit)" || return 1; \
      printf '%s\n' "${listing}" | while IFS= read -r line; do \
        case "${line}" in \
          "Alias name: "*) alias="${line#Alias name: }" ;; \
          "Owner: "*emSign*|"Owner: "*"AAA Certificate Services"*) echo "${alias}" ;; \
        esac; \
      done; \
    } && \
    aliases="$(distrusted)" && \
    chmod u+w "${KS}" && \
    printf '%s\n' "${aliases}" | sort -u | while IFS= read -r alias; do \
      [ -z "${alias}" ] || keytool -delete -alias "${alias}" -keystore "${KS}" -storepass changeit || exit 1; \
    done && \
    chmod a-w "${KS}" && \
    remaining="$(distrusted)" && \
    [ -z "${remaining}" ] && \
    rm -rf /tmp/hsperfdata_root
USER 1000

# Build with health and metrics endpoints enabled
RUN /opt/keycloak/bin/kc.sh build --health-enabled=true --metrics-enabled=true

# Use the default Keycloak entrypoint
ENTRYPOINT ["/opt/keycloak/bin/kc.sh"]
