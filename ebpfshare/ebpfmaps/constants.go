package ebpfmaps

const (
	NumOfPages = 2048

	// Logs eBPF map sizing constants.
	LogsMaxEntries = 256 * 1024 // 256KB - must match BPF log_events map size
)
