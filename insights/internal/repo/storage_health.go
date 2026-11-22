package repo

import "time"

// Storage health status values for the Storage page.
const (
	StorageStatusHealthy     = "healthy"
	StorageStatusDegraded    = "degraded"
	StorageStatusCritical    = "critical"
	StorageStatusUnreachable = "unreachable"
)

// StorageHealth is a point-in-time snapshot of Postgres + writeback health.
type StorageHealth struct {
	CheckedAt   time.Time
	Status      string
	Connection  StorageConnection
	Disk        StorageDisk
	Connections StorageConnections
	Database    StorageDatabaseStats
	Queries     StorageQueries
	Tables      []StorageTable
	Maintenance StorageMaintenance
	Retention   StorageRetention
	// Writeback is filled by the service (flush interval / last flush), not the DB.
	Writeback StorageWriteback
}

type StorageConnection struct {
	Reachable     bool
	PingLatencyMs int64
	Version       string
	Database      string
	Error         string
	UptimeSeconds float64
}

type StorageDisk struct {
	// DatabaseBytes is the size of the Insights database on disk.
	DatabaseBytes uint64
	// VolumeBytes is the size of the volume backing the database; 0 when
	// unknown (external Postgres).
	VolumeBytes uint64
	UsedPercent float64
	Status      string // ok | warning | critical
}

type StorageConnections struct {
	// Server-side connections to the Insights database, by state.
	Active            int
	Idle              int
	IdleInTransaction int
	Total             int
	// Max is the server's max_connections.
	Max int
	// Pool* describe this replica's client pool.
	PoolAcquired    int
	PoolIdle        int
	PoolTotal       int
	PoolMax         int
	PoolWaitCount   int64
	PoolWaitSeconds float64
}

type StorageDatabaseStats struct {
	CacheHitPercent float64
	Commits         int64
	Rollbacks       int64
	Deadlocks       int64
	TempBytes       int64
	// StatsSince is when the counters above were last reset.
	StatsSince time.Time
}

type StorageQueries struct {
	Active         int
	LongestSeconds float64
	// Running are the queries executing now, longest first.
	Running []StorageQuery
	// TopStatements are the statements with the most total execution time,
	// from pg_stat_statements. Empty when the extension is not installed.
	TopStatements       []StorageStatement
	StatementsAvailable bool
}

type StorageQuery struct {
	ElapsedSeconds float64
	User           string
	State          string
	WaitEvent      string
	Query          string
}

type StorageStatement struct {
	Query        string
	Calls        int64
	TotalSeconds float64
	MeanMs       float64
	Rows         int64
}

type StorageTable struct {
	Name        string
	Rows        int64
	DeadRows    int64
	BytesOnDisk uint64
	// LastVacuumAt is the newest manual or auto vacuum; zero when never.
	LastVacuumAt time.Time
}

type StorageMaintenance struct {
	DeadRows       int64
	VacuumsRunning int
	// WaitingLocks counts sessions blocked on a lock right now.
	WaitingLocks int
	// OldestTransactionSeconds is the age of the oldest open transaction.
	OldestTransactionSeconds float64
}

type StorageRetention struct {
	ObservationRetentionDays int
	RelationRetentionDays    int
	AttemptRetentionDays     int
	EverRan                  bool
	LastRunAt                time.Time
	LastRunOK                bool
	LastError                string
	ObservationsDeleted      int64
	AttemptsDeleted          int64
}

type StorageWriteback struct {
	FlushIntervalSeconds int
	LastFlushOK          bool
	LastFlushAt          time.Time
	LastFlushError       string
	EverFlushed          bool
}
