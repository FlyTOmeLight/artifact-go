// Command migrate installs the artifact and storage schemas without starting services.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/FlyTOmeLight/server-go/conf"
	"github.com/FlyTOmeLight/server-go/db"
	"github.com/FlyTOmeLight/server-go/logger"
	"github.com/go-sql-driver/mysql"
	"github.com/spf13/viper"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
	storagetable "github.com/FlyTOmeLight/artifact-go/internal/table/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("artifact-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String(
		"config",
		os.Getenv(conf.EnvServerConfigPath),
		"server config path (or SERVER_CONFIG_PATH); only DB and StorageDB are loaded",
	)
	timeout := flags.Duration("timeout", 2*time.Minute, "total migration timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || *timeout <= 0 {
		return errors.New("config and positive timeout required; positional arguments are not supported")
	}
	configs, err := loadDatabases(*path)
	if err != nil {
		return err
	}
	log, err := logger.NewLogger(&logger.Config{Level: "error", Outputs: []logger.Output{{Type: "console"}}})
	if err != nil {
		return err
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(logger.WithContext(ctx, log), *timeout)
	defer cancel()
	for i, cfg := range configs {
		name, migrate := "artifact", table.Migrate
		if i == 1 {
			name, migrate = "storage", storagetable.Migrate
		}
		if err := migrateDatabase(ctx, cfg, migrate); err != nil {
			return fmt.Errorf("%s migration: %w", name, err)
		}
	}
	_, err = fmt.Fprintln(stdout, `{"status":"migrated","domains":["artifact","storage"]}`)
	return err
}

func loadDatabases(path string) ([]db.Config, error) {
	input := viper.New()
	input.SetConfigFile(path)
	if err := input.ReadInConfig(); err != nil {
		return nil, errors.New("cannot read migration config")
	}
	// Resolve only database sections. Tenant HTTP and storage credentials are not
	// dependencies of a schema migration, even when they share the config file.
	settings := map[string]any{"AppName": "artifact-migrate", "AppCode": 1, "RunMode": "release"}
	for _, key := range []string{"DB", "StorageDB"} {
		if !input.IsSet(key) {
			return nil, fmt.Errorf("%s is required", key)
		}
		settings[key] = input.Get(key)
	}
	resolved, err := conf.FromSettings(settings)
	if err != nil {
		return nil, errors.New("cannot resolve database configuration")
	}
	configs := make([]db.Config, 2)
	for i, key := range []string{"DB", "StorageDB"} {
		cfg := &configs[i]
		if err := resolved.UnmarshalKey(key, cfg); err != nil {
			return nil, fmt.Errorf("invalid %s configuration", key)
		}
		cfg.AutoMigrate = false
		if cfg.Driver != db.DriverPostgreSQL && cfg.Driver != db.DriverMySQL {
			return nil, fmt.Errorf("%s requires PostgreSQL or MySQL", key)
		}
		if cfg.Driver == db.DriverMySQL {
			if cfg.DSNParams == "" {
				cfg.DSNParams = "charset=utf8mb4&parseTime=true&loc=UTC&interpolateParams=true"
			}
			parsed, err := mysql.ParseDSN(mysqlDSNPrefix + cfg.DSNParams)
			if err != nil || !parsed.ParseTime || parsed.Loc != time.UTC {
				return nil, fmt.Errorf("%s MySQL requires parseTime=true and loc=UTC", key)
			}
		}
	}
	return configs, nil
}

func migrateDatabase(ctx context.Context, cfg db.Config, migrate func(context.Context, *db.DB) error) (err error) {
	if cfg.Driver == db.DriverMySQL {
		// db.NewDB has no dialector hook: GORM's MySQL initializer runs
		// "SELECT VERSION()" with context.Background(). Driver timeouts are the
		// only way to bound that handshake, and they also bound genuinely stuck
		// network I/O for every later connection in the pool.
		if deadline, ok := ctx.Deadline(); ok {
			cfg.DSNParams = mysqlTimeoutParams(cfg.DSNParams, time.Until(deadline))
		}
	}
	database, err := db.NewDB(ctx, &cfg)
	if err != nil {
		return err
	}
	connection, err := database.DB()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, connection.Close()) }()
	return migrate(ctx, database)
}

const mysqlDSNPrefix = "artifact@tcp(localhost:3306)/artifact?"

// mysqlTimeoutParams clamps the MySQL driver's dial and I/O timeouts to limit so
// --timeout and cancellation reach the handshake and version query. Values from
// the configuration are kept when they are already shorter.
func mysqlTimeoutParams(params string, limit time.Duration) string {
	limit = max(limit, time.Nanosecond)
	limitText := limit.String()
	timeouts := map[string]string{
		"timeout":      limitText,
		"readTimeout":  limitText,
		"writeTimeout": limitText,
	}
	known := make(map[string]bool, len(timeouts))
	for key := range timeouts {
		known[key] = true
	}
	var kept []string
	for param := range strings.SplitSeq(params, "&") {
		key, value, found := strings.Cut(param, "=")
		if !found || param == "" {
			continue
		}
		if known[key] {
			duration, err := time.ParseDuration(value)
			if err == nil && duration > 0 && duration < limit {
				timeouts[key] = value
			}
			continue
		}
		kept = append(kept, param)
	}
	kept = append(
		kept,
		"timeout="+timeouts["timeout"],
		"readTimeout="+timeouts["readTimeout"],
		"writeTimeout="+timeouts["writeTimeout"],
	)
	return strings.Join(kept, "&")
}
