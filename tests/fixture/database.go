// Package fixture owns real database and HTTP resources for integration tests.
package fixture

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/host"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/google/uuid"

	"github.com/FlyTOmeLight/artifact-go/service"
)

// ForEachDatabase runs the same behavior against both supported databases.
// make check starts isolated containers and requires both configurations.
func ForEachDatabase(t *testing.T, run func(*testing.T, *db.DB)) {
	t.Helper()
	for _, driver := range []string{db.DriverPostgreSQL, db.DriverMySQL} {
		t.Run(driver, func(t *testing.T) { run(t, Open(t, driver)) })
	}
}

// Open creates an isolated schema/database and drops it after its connections close.
func Open(t *testing.T, driver string) *db.DB {
	t.Helper()
	cfg := Config(t, driver)
	root, err := db.NewDB(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rootSQL.Close() })
	name := "artifact_test_" + uuid.NewString()[:8]
	if driver == db.DriverPostgreSQL {
		if err := root.Exec("CREATE SCHEMA " + name).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := root.Exec("DROP SCHEMA " + name + " CASCADE").Error; err != nil {
				t.Error(err)
			}
		})
		cfg.DSN += "&search_path=" + name
	} else {
		if err := root.Exec(
			"CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin",
		).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := root.Exec("DROP DATABASE " + name).Error; err != nil {
				t.Error(err)
			}
		})
		cfg.DB = name
	}
	database, err := db.NewDB(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return database
}

// Config reads only disposable databases supplied by the check runner.
func Config(t *testing.T, driver string) db.Config {
	t.Helper()
	cfg := db.Config{Driver: driver, MaxOpenConns: 10, MaxIdleConns: 5}
	if driver == db.DriverPostgreSQL {
		cfg.DSN = os.Getenv("ARTIFACT_TEST_PG_DSN")
		if cfg.DSN == "" {
			t.Skip("PostgreSQL integration requires make check")
		}
	} else {
		port := os.Getenv("ARTIFACT_TEST_MYSQL_PORT")
		if port == "" {
			t.Skip("MySQL integration requires make check")
		}
		var err error
		cfg.Port, err = strconv.Atoi(port)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Host = "127.0.0.1"
		cfg.User = "root"
		cfg.Password = "artifact-test-only"
		cfg.DB = "artifact_test"
		cfg.DSNParams = "charset=utf8mb4&parseTime=true&loc=UTC&interpolateParams=true"
	}
	return cfg
}

// HTTP uses the production framework host and injected trusted identity boundary.
func HTTP(t *testing.T, database *db.DB, resolver service.IdentityResolver) *httptest.Server {
	t.Helper()
	return HTTPWithOptions(t, database, resolver, nil)
}

// HTTPWithOptions serves the same database with explicit deployment JSON limits.
func HTTPWithOptions(
	t *testing.T,
	database *db.DB,
	resolver service.IdentityResolver,
	limits *service.VersionConfig,
) *httptest.Server {
	t.Helper()
	svc, err := service.New(
		context.Background(),
		database,
		service.Options{IdentityResolver: resolver, AutoMigrate: true, Version: limits},
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &conf.Config{AppName: "artifact-test", AppCode: 100, RunMode: conf.RunModeTest}
	manager, err := conf.NewManagerFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	log, err := logger.NewLogger(&logger.Config{Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(log.Close)
	h, err := host.NewHTTPHost(manager, log, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(fmt.Errorf("close host: %w", err))
		}
	})
	server := httptest.NewServer(h.Handler())
	t.Cleanup(server.Close)
	return server
}
