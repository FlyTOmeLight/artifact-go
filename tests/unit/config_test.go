package unit_test

import (
	"testing"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/service"
)

func TestVersionLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits *service.VersionConfig
		valid  bool
	}{
		{name: "defaults", valid: true},
		{name: "positive", limits: &service.VersionConfig{MaxFilesPerVersion: 1000, MaxPayloadBytes: 256, MaxAnnotationsBytes: 64, MaxRequestBodyBytes: 1024}, valid: true},
		{name: "missing-files", limits: &service.VersionConfig{MaxPayloadBytes: 256, MaxAnnotationsBytes: 64, MaxRequestBodyBytes: 1024}, valid: false},
		{name: "negative-files", limits: &service.VersionConfig{MaxFilesPerVersion: -1, MaxPayloadBytes: 256, MaxAnnotationsBytes: 64, MaxRequestBodyBytes: 1024}, valid: false},
		{name: "zero", limits: &service.VersionConfig{}, valid: false},
		{name: "negative", limits: &service.VersionConfig{MaxFilesPerVersion: 1000, MaxPayloadBytes: -1, MaxAnnotationsBytes: 64, MaxRequestBodyBytes: 1024}, valid: false},
		{name: "missing-body", limits: &service.VersionConfig{MaxFilesPerVersion: 1000, MaxPayloadBytes: 256, MaxAnnotationsBytes: 64}, valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := (service.Config{DB: db.Config{Driver: db.DriverPostgreSQL}, Version: tc.limits}).Validate()
			if (err == nil) != tc.valid {
				t.Fatalf("Validate=%v valid=%v", err, tc.valid)
			}
		})
	}
}
