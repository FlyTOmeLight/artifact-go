package doctor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/table"
)

const (
	DefaultLimit = 1000
	MaxLimit     = 10000
)

type Finding struct {
	ID       string `json:"id"`
	TaskID   string `json:"taskID,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Check struct {
	Name      string    `json:"name"`
	Scanned   int       `json:"scanned"`
	Truncated bool      `json:"truncated"`
	Findings  []Finding `json:"findings"`
}

type Report struct {
	Domain       string    `json:"domain"`
	DatabaseTime time.Time `json:"databaseTime"`
	Limit        int       `json:"limit"`
	Complete     bool      `json:"complete"`
	Errors       int       `json:"errors"`
	Warnings     int       `json:"warnings"`
	Checks       []Check   `json:"checks"`
}

func Run(ctx context.Context, database *db.DB, domain string, limit int) (*Report, error) {
	if domain != "artifact" && domain != "storage" {
		return nil, fmt.Errorf("unsupported doctor domain %q", domain)
	}
	if limit < 1 || limit > MaxLimit {
		return nil, fmt.Errorf("doctor limit must be between 1 and %d", MaxLimit)
	}
	if database == nil {
		return nil, errors.New("doctor requires a database")
	}
	if driver := database.Dialector.Name(); driver != "postgres" && driver != "mysql" {
		return nil, errors.New("doctor requires PostgreSQL or MySQL")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	report := &Report{Domain: domain, Limit: limit, Complete: true}
	err := db.Transaction(ctx, database, db.TransactionOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	}, func(tx *db.DB) error {
		var err error
		report.DatabaseTime, err = table.DatabaseNow(tx)
		if err != nil {
			return err
		}
		if domain == "storage" {
			report.Checks, err = storageChecks(tx, limit)
		} else {
			report.Checks, err = artifactChecks(tx, report.DatabaseTime, limit)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, check := range report.Checks {
		report.Complete = report.Complete && !check.Truncated
		for _, finding := range check.Findings {
			if finding.Severity == "error" {
				report.Errors++
			} else {
				report.Warnings++
			}
		}
	}
	return report, nil
}

func candidates[Row any](query *db.DB, name string, limit int) ([]Row, Check, error) {
	check := Check{Name: name, Findings: []Finding{}}
	var rows []Row
	if err := query.Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, check, fmt.Errorf("%s: %w", name, err)
	}
	check.Truncated = len(rows) > limit
	if check.Truncated {
		rows = rows[:limit]
	}
	check.Scanned = len(rows)
	return rows, check, nil
}
