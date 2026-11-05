package coordinator

import (
	"context"
	"testing"
	"time"
)

// TestSettleTriggersBatchesABurst: triggers that keep arriving within the quiet period are absorbed, and the pass
// starts once they stop.
func TestSettleTriggersBatchesABurst(t *testing.T) {
	trigger := make(chan struct{}, 1)
	quiet := 60 * time.Millisecond
	go func() {
		for range 4 {
			time.Sleep(20 * time.Millisecond)
			trigger <- struct{}{}
		}
	}()
	start := time.Now()
	if !settleTriggers(context.Background(), trigger, quiet, time.Second) {
		t.Fatal("settle reported a cancelled context")
	}
	elapsed := time.Since(start)
	if elapsed < 80*time.Millisecond+quiet || elapsed > 500*time.Millisecond {
		t.Fatalf("settled after %v, want just past the last trigger plus %v", elapsed, quiet)
	}
	if len(trigger) != 0 {
		t.Fatal("a trigger from the burst was left for another pass")
	}
}

// TestSettleTriggersIsBounded: a steady stream of changes cannot hold the pass off past the bound.
func TestSettleTriggersIsBounded(t *testing.T) {
	trigger := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				select {
				case trigger <- struct{}{}:
				default:
				}
			}
		}
	}()
	start := time.Now()
	if !settleTriggers(context.Background(), trigger, 50*time.Millisecond, 150*time.Millisecond) {
		t.Fatal("settle reported a cancelled context")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond || elapsed > 400*time.Millisecond {
		t.Fatalf("settled after %v, want the 150ms bound", elapsed)
	}
}

func TestSettleTriggersStopsWithTheLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if settleTriggers(ctx, make(chan struct{}), time.Second, time.Second) {
		t.Fatal("settle ran a pass after the loop stopped")
	}
}
