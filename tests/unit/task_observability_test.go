package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/FlyTOmeLight/artifact-go/internal/telemetry"
)

func TestTaskCollectorStopWaitsForBorrowedDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{})
	released := make(chan struct{})
	cancelled := make(chan struct{})
	var reads atomic.Int32
	unregister, err := telemetry.RegisterTasks(
		ctx,
		"storage",
		[]string{"delete"},
		func(ctx context.Context) (telemetry.TaskSnapshot, error) {
			if reads.Add(1) == 1 {
				close(entered)
			}
			<-ctx.Done()
			close(cancelled)
			<-released
			return telemetry.TaskSnapshot{}, ctx.Err()
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Another service can continue scraping the shared registry during Stop.
	other, err := telemetry.RegisterTasks(
		t.Context(),
		"artifact",
		[]string{"retention"},
		func(context.Context) (telemetry.TaskSnapshot, error) {
			return telemetry.TaskSnapshot{Now: time.Now()}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer other()
	gathered := make(chan error, 1)
	go func() { _, err := prometheus.DefaultGatherer.Gather(); gathered <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot not entered")
	}
	cancel()
	<-cancelled
	stopped := make(chan struct{})
	go func() { unregister(); close(stopped) }()
	// The snapshot deliberately retains its DB borrow after observing cancellation.
	select {
	case <-stopped:
		t.Fatal("Stop returned before database borrower exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(released)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish")
	}
	if err := <-gathered; err != nil {
		t.Fatal(err)
	}
	if _, err := prometheus.DefaultGatherer.Gather(); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 {
		t.Fatal("scrape started a database read after Stop")
	}
}
