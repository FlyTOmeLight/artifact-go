package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/service"
	"github.com/FlyTOmeLight/artifact-go/tests/fixture"
)

func TestMySQLTimeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		valid        bool
	}{
		{name: "default", params: "", valid: true},
		{name: "utc", params: "parseTime=true&loc=UTC", valid: true},
		{name: "local", params: "parseTime=true&loc=Local", valid: false},
		{name: "other_zone", params: "parseTime=true&loc=Asia%2FShanghai", valid: false},
		{name: "bytes", params: "parseTime=false&loc=UTC", valid: false},
		{name: "invalid", params: "parseTime=invalid&loc=UTC", valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := service.Config{DB: db.Config{Driver: db.DriverMySQL, DSNParams: tc.params}}
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() = %v; valid = %v", err, tc.valid)
			}
		})
	}
}

func TestBorrowedMySQLRequiresUTC(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		valid        bool
	}{
		{name: "utc", params: "parseTime=true&loc=UTC", valid: true},
		{name: "local", params: "parseTime=true&loc=Local", valid: false},
		{name: "other_zone", params: "parseTime=true&loc=Japan", valid: false},
		{name: "bytes", params: "parseTime=false&loc=UTC", valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fixture.Config(t, db.DriverMySQL)
			cfg.DSNParams = tc.params
			database, err := db.NewDB(context.Background(), &cfg)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := database.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			resolve := func(*http.Request) (service.RequestIdentity, error) { return service.RequestIdentity{}, nil }
			_, err = service.New(context.Background(), database, service.Options{IdentityResolver: resolve})
			if (err == nil) != tc.valid {
				t.Fatalf("New() = %v; valid = %v", err, tc.valid)
			}
			if err != nil && !strings.Contains(err.Error(), "loc=UTC") {
				t.Fatalf("unexpected error: %v", err)
			}
			if err := conn.Ping(); err != nil {
				t.Fatalf("borrowed connection closed: %v", err)
			}
		})
	}
}

func TestServiceRequiresTrustedIdentity(t *testing.T) {
	if err := service.Build(context.Background(), nil, &service.Config{}); err == nil {
		t.Fatal("missing resolver accepted")
	}
	if _, err := service.New(context.Background(), nil, service.Options{}); err == nil {
		t.Fatal("missing database accepted")
	}
	fixture.ForEachDatabase(t, func(t *testing.T, database *db.DB) {
		if _, err := service.New(context.Background(), database, service.Options{}); err == nil {
			t.Fatal("missing resolver accepted")
		}
		resolve := func(*http.Request) (service.RequestIdentity, error) { return service.RequestIdentity{}, nil }
		svc, err := service.New(
			context.Background(),
			database,
			service.Options{IdentityResolver: resolve, AutoMigrate: true},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := svc.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := svc.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		sqlDB, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Ping(); err != nil {
			t.Fatalf("service closed borrowed database: %v", err)
		}
		server := fixture.HTTP(t, database, resolve)
		res, err := http.Get(server.URL + "/v1/artifacts")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 401 {
			t.Fatalf("empty identity accepted: %d", res.StatusCode)
		}
	})
}
