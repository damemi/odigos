# Build the manager binary
FROM quay.io/operator-framework/helm-operator:v1.39.1

ENV HOME=/opt/helm
# Meant to be run from odigos root
COPY operator/watches.yaml ${HOME}/watches.yaml
COPY helm  ${HOME}/helm-charts
WORKDIR ${HOME}
