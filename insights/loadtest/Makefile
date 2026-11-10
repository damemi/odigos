# Insights OTLP loadtest (k6 Deployment in-cluster).
#
#   make start
#   VUS=20 SLEEP_MS=10 make start
#   make stop
#   make restart

.PHONY: start stop restart help

NS ?= insights-loadtest
VUS ?=
DURATION ?=
SLEEP_MS ?=
ENDPOINT ?=
TENANTS ?=

help:
	@./run.sh help

## start: apply namespace + script ConfigMap + k6 Deployment
start:
	NS=$(NS) VUS="$(VUS)" DURATION="$(DURATION)" SLEEP_MS="$(SLEEP_MS)" ENDPOINT="$(ENDPOINT)" TENANTS="$(TENANTS)" ./run.sh start

## stop: delete the k6 Deployment
stop:
	NS=$(NS) ./run.sh stop