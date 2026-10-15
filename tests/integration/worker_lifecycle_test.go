package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	api "github.com/FlyTOmeLight/artifact-go/sdk/go/artifactv1"
	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestServiceWorkerLifecycle(t *testing.T) {
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		server := fixture.HTTP(t, database, resolve)
		c := versionClient(t, server.URL)
		ctx := identity(t, "tenant")
		a := create(t, c, ctx, "worker-lifecycle", "model")
		ap := api.ArtifactPath{ArtifactID: a.ID}
		v := publishVersion(t, c, a.ID, "old")
		if err := database.Exec(
			"UPDATE versions SET published_at = ? WHERE id = ?",
			v.PublishedAt.Add(-time.Hour),
			v.ID,
		).Error; err != nil {
			t.Fatal(err)
		}
		policy := policyRequest(a.ID, 0, nil, api.OverflowDeleteOldest)
		policy.MaxAgeSeconds = revision(60)
		policy.MinVersions = revision(0)
		if _, err := c.UpdateRetention(ctx, policy); err != nil {
			t.Fatal(err)
		}
		svc, err := service.New(
			t.Context(),
			database,
			service.Options{
				IdentityResolver: resolve,
				Worker: &service.WorkerConfig{
					Concurrency:   2,
					ScanInterval:  20 * time.Millisecond,
					LeaseDuration: time.Second,
					RenewInterval: 200 * time.Millisecond,
					MaxFailures:   3,
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err = svc.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if e := svc.Stop(stop); e != nil {
				t.Error(e)
			}
		})
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			versions, e := c.ListVersions(deadline, &api.ListVersionsRequest{ArtifactPath: ap})
			if e != nil {
				t.Fatal(e)
			}
			if len(versions.Items) == 0 {
				break
			}
			select {
			case <-deadline.Done():
				t.Fatal("service worker did not execute scheduled age retention")
			case <-ticker.C:
			}
		}
		stop, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		if err = svc.Stop(stop); err != nil {
			t.Fatal(err)
		}
		// Stop waits for owned goroutines but leaves the caller's database usable.
		var value int
		if err = database.Raw("SELECT 1").Scan(&value).Error; err != nil || value != 1 {
			t.Fatalf("borrowed database closed value=%d err=%v", value, err)
		}
		if _, err = c.GetArtifact(ctx, &api.GetArtifactRequest{ArtifactPath: ap}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWorkerConfigurationValidation(t *testing.T) {
	valid := service.WorkerConfig{
		Concurrency:   1,
		ScanInterval:  time.Second,
		LeaseDuration: time.Minute,
		RenewInterval: 10 * time.Second,
		MaxFailures:   3,
	}
	cfg := service.Config{DB: db.Config{Driver: db.DriverPostgreSQL}, Worker: &valid}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"concurrency", "scan", "lease", "renew", "failures", "renew-equals-lease", "renew-exceeds-lease"} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			switch name {
			case "concurrency":
				bad.Concurrency = 0
			case "scan":
				bad.ScanInterval = 0
			case "lease":
				bad.LeaseDuration = 0
			case "renew":
				bad.RenewInterval = 0
			case "failures":
				bad.MaxFailures = 0
			case "renew-equals-lease":
				bad.RenewInterval = bad.LeaseDuration
			case "renew-exceeds-lease":
				bad.RenewInterval = 2 * bad.LeaseDuration
			}
			cfg.Worker = &bad
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid worker configuration accepted")
			}
		})
	}
}
