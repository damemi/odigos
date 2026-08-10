// Package ebpfshare hosts the eBPF map exchange between the enterprise odiglet and the
// enterprise collector: odiglet creates the shared maps and serves their file descriptors
// over a unix socket, and odigosebpfreceiver reads telemetry out of them.
//
// It lives here rather than in odigos OSS because both ends of the exchange are
// enterprise-only, and it is a module of its own because the odiglet and the collector
// components that consume it are separate modules with no shared parent.
package ebpfshare
