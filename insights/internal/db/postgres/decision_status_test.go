package postgres

import (
	"testing"
	"time"
)

func TestStickyDecisionStatus(t *testing.T) {
	t1 := time.Unix(100, 0)
	t2 := time.Unix(200, 0)
	t3 := time.Unix(300, 0)

	cases := []struct {
		name                        string
		terminal, dismissed, reopen time.Time
		wantTerminal, wantDismissed bool
	}{
		{
			name:     "all unset is open",
			terminal: epoch, dismissed: epoch, reopen: epoch,
		},
		{
			name:     "accept after dismiss and reopen is terminal",
			terminal: t3, dismissed: t1, reopen: t2,
			wantTerminal: true,
		},
		{
			name:     "dismiss after reopen is dismissed",
			terminal: epoch, dismissed: t2, reopen: t1,
			wantDismissed: true,
		},
		{
			name:     "reopen after dismiss is open",
			terminal: epoch, dismissed: t1, reopen: t2,
		},
		{
			name:     "accept ties dismiss - accept wins",
			terminal: t2, dismissed: t2, reopen: t1,
			wantTerminal: true,
		},
		{
			name:     "accept older than reopen is not sticky",
			terminal: t1, dismissed: epoch, reopen: t2,
		},
		{
			name:     "dismiss equal to reopen is not dismissed",
			terminal: epoch, dismissed: t2, reopen: t2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			terminal, dismissed := stickyDecisionStatus(tc.terminal, tc.dismissed, tc.reopen)
			if terminal != tc.wantTerminal || dismissed != tc.wantDismissed {
				t.Fatalf("got terminal=%v dismissed=%v, want terminal=%v dismissed=%v",
					terminal, dismissed, tc.wantTerminal, tc.wantDismissed)
			}
		})
	}
}
