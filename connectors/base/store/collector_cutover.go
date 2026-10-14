package store

import (
	"context"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcutover"
)

// CutoverAnnouncement records the precise destination and Collector readiness event for which
// the first workload rollout was announced. The row is retained across connector restarts.
type CutoverAnnouncement struct {
	MarkerUpdatedAt time.Time
	FirstReadyAt    time.Time
	AnnouncedAt     time.Time
	Announcing      bool
}

func (a CutoverAnnouncement) Matches(markerUpdatedAt, firstReadyAt time.Time) bool {
	return a.MarkerUpdatedAt.Equal(markerUpdatedAt) && a.FirstReadyAt.Equal(firstReadyAt)
}

func (s *Store) GetCollectorCutoverAnnouncement(ctx context.Context, region string) (*CutoverAnnouncement, error) {
	var announcement CutoverAnnouncement
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT marker_updated_at, first_ready_at, announced_at,
		       announced_at > now() - ($3::int * interval '1 second')
		FROM %s.collector_cutover_announcements
		WHERE connector_name = $1 AND region = $2
	`, s.schema), s.connectorName, region, int(collectorcutover.AnnouncementWindow.Seconds())).Scan(
		&announcement.MarkerUpdatedAt, &announcement.FirstReadyAt, &announcement.AnnouncedAt, &announcement.Announcing)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &announcement, nil
}

// EnsureCollectorCutoverAnnouncement starts a new warning window only when the selected marker
// or first-ready event changes. The upsert is atomic across Fargate and Lambda reconcilers.
func (s *Store) EnsureCollectorCutoverAnnouncement(ctx context.Context, region string, markerUpdatedAt, firstReadyAt time.Time) (bool, error) {
	var announcing bool
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO %s.collector_cutover_announcements AS current
			(connector_name, region, marker_updated_at, first_ready_at, announced_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (connector_name, region) DO UPDATE SET
			marker_updated_at = EXCLUDED.marker_updated_at,
			first_ready_at = EXCLUDED.first_ready_at,
			announced_at = CASE
				WHEN current.marker_updated_at IS DISTINCT FROM EXCLUDED.marker_updated_at
				  OR current.first_ready_at IS DISTINCT FROM EXCLUDED.first_ready_at
				THEN now() ELSE current.announced_at END
		RETURNING announced_at > now() - ($5::int * interval '1 second')
	`, s.schema), s.connectorName, region, markerUpdatedAt, firstReadyAt,
		int(collectorcutover.AnnouncementWindow.Seconds())).Scan(&announcing)
	return announcing, err
}
