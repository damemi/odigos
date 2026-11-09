// Package leader runs a function on exactly one replica at a time using a
// Kubernetes Lease. It gates the singleton jobs (the findings notifier, the
// recommendation sweeps) while every other part of the service stays
// replica-symmetric.
package leader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// Lease timings. A follower takes over within LeaseDuration of a leader
// vanishing; the leader renews every RetryPeriod and gives up (and stops
// its work) if it cannot renew within RenewDeadline.
const (
	LeaseDuration = 15 * time.Second
	RenewDeadline = 10 * time.Second
	RetryPeriod   = 2 * time.Second
)

// Config identifies the lease and this replica.
type Config struct {
	// LeaseName is the coordination.k8s.io Lease object name.
	LeaseName string
	// Namespace defaults to CURRENT_NS.
	Namespace string
	// Identity defaults to POD_NAME, then hostname.
	Identity string
}

// Elector acquires the lease and runs the leader callback.
type Elector struct {
	cfg    Config
	client kubernetes.Interface
	log    *slog.Logger
}

// ErrNotInCluster is returned by New when no in-cluster credentials exist,
// so callers can fall back to running the work unconditionally (local dev,
// single-process tests).
var ErrNotInCluster = errors.New("leader: not running in a cluster")

// New builds an Elector from in-cluster config and the environment.
func New(cfg Config, log *slog.Logger) (*Elector, error) {
	if cfg.LeaseName == "" {
		return nil, errors.New("leader: LeaseName is required")
	}
	if cfg.Namespace == "" {
		cfg.Namespace = os.Getenv("CURRENT_NS")
	}
	if cfg.Namespace == "" {
		return nil, errors.New("leader: namespace is required (CURRENT_NS)")
	}
	if cfg.Identity == "" {
		cfg.Identity = Identity()
	}
	rc, err := rest.InClusterConfig()
	if err != nil {
		if errors.Is(err, rest.ErrNotInCluster) {
			return nil, ErrNotInCluster
		}
		return nil, fmt.Errorf("leader: in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("leader: build client: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Elector{cfg: cfg, client: client, log: log.With("component", "leader", "lease", cfg.LeaseName)}, nil
}

// Identity returns this replica's election identity: POD_NAME, else hostname.
func Identity() string {
	if id := os.Getenv("POD_NAME"); id != "" {
		return id
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "unknown"
	}
	return host
}

// Run blocks until ctx is cancelled, repeatedly campaigning for the lease.
// run is invoked with a context that is cancelled when leadership is lost;
// it must return promptly on cancellation. onChange, when set, is called with
// true on acquisition and false on loss (metrics/debug UI).
func (e *Elector) Run(ctx context.Context, run func(ctx context.Context), onChange func(leader bool)) {
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Name: e.cfg.LeaseName, Namespace: e.cfg.Namespace},
		Client:     e.client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: e.cfg.Identity},
	}
	for ctx.Err() == nil {
		// RunOrDie returns when leadership is lost; loop to campaign again so
		// a replica that lost its lease (API blip) can win it back.
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:            lock,
			ReleaseOnCancel: true,
			LeaseDuration:   LeaseDuration,
			RenewDeadline:   RenewDeadline,
			RetryPeriod:     RetryPeriod,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(lctx context.Context) {
					e.log.Info("acquired leadership", "identity", e.cfg.Identity)
					if onChange != nil {
						onChange(true)
					}
					run(lctx)
				},
				OnStoppedLeading: func() {
					e.log.Info("lost leadership", "identity", e.cfg.Identity)
					if onChange != nil {
						onChange(false)
					}
				},
				OnNewLeader: func(id string) {
					if id != e.cfg.Identity {
						e.log.Info("observed leader", "leader", id)
					}
				},
			},
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(RetryPeriod):
		}
	}
}
