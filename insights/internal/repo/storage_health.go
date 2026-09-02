package repo

import "time"

// Storage health status values for the debug Storage page.
const (
	StorageStatusHealthy     = "healthy"
	StorageStatusDegraded    = "degraded"
	StorageStatusCritical    = "critical"
	StorageStatusUnreachable = "unreachable"
)

// StorageHealth is a point-in-time snapshot of ClickHouse + writeback health.
type StorageHealth struct {
	CheckedAt     time.Time
	Status        string
	Connection    StorageConnection
	Disk          StorageDisk
	Memory        StorageMemory
	Queries       StorageQueries
	Tables        []StorageTable
	WritePressure StorageWritePressure
	// Writeback is filled by the service (flush interval / last flush), not CH.
	Writeback StorageWriteback
}

type StorageConnection struct {
	Reachable     bool
	PingLatencyMs int64
	Version       string
	Database      string
	Error         string
}

type StorageDisk struct {
	// InsightsBytes is sum(bytes_on_disk) for Insights tables — the accurate
	// "how much data do we store?" figure. Filesystem UsedBytes can be much
	// larger on shared volumes (kind / hostPath).
	InsightsBytes  uint64
	TotalBytes     uint64
	UsedBytes      uint64
	AvailableBytes uint64
	UsedPercent    float64
	Status         string // ok | warning | critical
}

type StorageMemory struct {
	ProcessRSSBytes  uint64
	CGroupUsedBytes  uint64
	CGroupTotalBytes uint64 // 0 = no cgroup limit
	UptimeSeconds    float64
}

type StorageQueries struct {
	Active          int
	LongestSeconds  float64
	PeakMemoryBytes uint64
	Items           []StorageQuery // currently running
	RecentHeaviest  []StorageQuery // top by duration (then memory), last 5m
	RecentLatest    []StorageQuery // most recently finished, last 5m
}

type StorageQuery struct {
	ElapsedSeconds float64
	User           string
	MemoryBytes    uint64
	Query          string
	// FinishedAt is set for query_log rows; zero for currently running queries.
	FinishedAt time.Time
}

type StorageTable struct {
	Name        string
	Rows        uint64
	BytesOnDisk uint64
	ActiveParts uint64
}

type StorageWritePressure struct {
	UnfinishedMutations uint64
	ActiveParts         uint64
}

type StorageWriteback struct {
	FlushIntervalSeconds int
	LastFlushOK          bool
	LastFlushAt          time.Time
	LastFlushError       string
	EverFlushed          bool
}
