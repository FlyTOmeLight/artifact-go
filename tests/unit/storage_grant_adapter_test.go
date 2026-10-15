package unit_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FlyTOmeLight/artifact-go/internal/storage/client"
)

func TestStorageGrantAdapter(t *testing.T) {
	for _, mode := range []string{"success", "long", "expired", "missing", "failure"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_ = r.ParseForm()
				if r.Form.Get("Action") != "AssumeRole" ||
					!strings.Contains(r.Form.Get("Policy"), "arn:aws:s3:::bucket/key") ||
					!strings.Contains(r.Header.Get("Authorization"), "issuer") {
					t.Error("wrong policy or issuer")
				}
				expiry := time.Now().Add(905 * time.Second)
				if mode == "long" {
					expiry = time.Now().Add(time.Hour)
				}
				if mode == "expired" {
					expiry = time.Now().Add(-time.Second)
				}
				if mode == "failure" {
					w.WriteHeader(500)
					fmt.Fprint(w, "secret-sentinel")
					return
				}
				token := "session"
				if mode == "missing" {
					token = ""
				}
				fmt.Fprintf(
					w,
					`<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>issued</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`,
					token,
					expiry.UTC().Format(time.RFC3339),
				)
			}))
			defer endpoint.Close()
			c, err := client.New(
				client.Config{
					BackendID:       "fixture",
					Endpoint:        endpoint.URL,
					Region:          "us-east-1",
					AccessKeyID:     "admin",
					SecretAccessKey: "admin-secret",
					GrantIssuer: &client.GrantIssuerConfig{
						Endpoint:        endpoint.URL,
						AccessKeyID:     "issuer",
						SecretAccessKey: "issuer-secret",
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			request := client.GrantRequest{
				Bucket:   "bucket",
				Keys:     []string{"key"},
				Actions:  []string{"s3:PutObject"},
				Deadline: time.Now().Add(910 * time.Second),
			}
			result, err := c.CreateGrant(t.Context(), request)
			if mode == "success" {
				if err != nil || result.AccessKeyID != "issued" {
					t.Fatalf("grant: %v", err)
				}
			} else if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatalf("unexpected error: %v", err)
			}
			if calls != 1 {
				t.Fatalf("calls %d", calls)
			}
			request.Deadline = time.Now().Add(900 * time.Second)
			if _, err = c.CreateGrant(t.Context(), request); err == nil || calls != 1 {
				t.Fatal("short TTL sent")
			}
			request.Deadline = time.Now().Add(time.Hour)
			request.Keys = []string{strings.Repeat("a", 2100)}
			if _, err = c.CreateGrant(t.Context(), request); err == nil || calls != 1 {
				t.Fatal("oversized policy sent")
			}
			request.Keys = []string{"wild*"}
			if _, err = c.CreateGrant(t.Context(), request); err == nil || calls != 1 {
				t.Fatal("wildcard sent")
			}
			request.Keys = []string{"key"}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = c.CreateGrant(ctx, request)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel cause: %v", err)
			}
		})
	}
}

func TestStorageGrantIssuerValidation(t *testing.T) {
	for _, issuer := range []client.GrantIssuerConfig{
		{Endpoint: "http://localhost", AccessKeyID: "admin", SecretAccessKey: "secret"},
		{Endpoint: "http://localhost", AccessKeyID: "issuer", SecretAccessKey: "secret", SessionToken: "temporary"},
		{Endpoint: "http://localhost", AccessKeyID: "issuer", SecretAccessKey: "secret", ExpiresAt: time.Now().Add(-time.Second)},
		{Endpoint: "http://user:password@localhost", AccessKeyID: "issuer", SecretAccessKey: "secret"},
	} {
		_, err := client.New(
			client.Config{
				BackendID:       "fixture",
				Endpoint:        "http://localhost",
				Region:          "us-east-1",
				AccessKeyID:     "admin",
				SecretAccessKey: "secret",
				GrantIssuer:     &issuer,
			},
		)
		if err == nil {
			t.Fatal("invalid issuer accepted")
		}
	}
	issuer := client.GrantIssuerConfig{
		Endpoint:        "http://127.0.0.1:1",
		AccessKeyID:     "issuer",
		SecretAccessKey: "secret",
		ExpiresAt:       time.Now().Add(5 * time.Minute),
	}
	c, err := client.New(
		client.Config{
			BackendID:       "fixture",
			Endpoint:        "http://localhost",
			Region:          "us-east-1",
			AccessKeyID:     "admin",
			SecretAccessKey: "secret",
			GrantIssuer:     &issuer,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateGrant(
		t.Context(),
		client.GrantRequest{
			Bucket:   "bucket",
			Keys:     []string{"key"},
			Actions:  []string{"s3:GetObject"},
			Deadline: time.Now().Add(time.Hour),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "900") {
		t.Fatalf("issuer TTL not enforced: %v", err)
	}
}
