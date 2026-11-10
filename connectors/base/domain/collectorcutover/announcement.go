package collectorcutover

import "time"

// AnnouncementWindow keeps the first workload write at least one reconcile interval after
// the first eligible workload appears. AnnouncedAt is persisted, so restarts do not skip it.
const AnnouncementWindow = time.Minute
