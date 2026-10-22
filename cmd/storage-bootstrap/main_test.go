package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	madmin "github.com/minio/madmin-go/v2"
)

func TestCredentialAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("test-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOOTSTRAP_TEST_KEY_FILE", path)
	if value, err := credential("BOOTSTRAP_TEST_KEY"); err != nil || value != "test-value" {
		t.Fatal("file credential", err)
	}
	t.Setenv("BOOTSTRAP_TEST_KEY", "env-value")
	if _, err := credential("BOOTSTRAP_TEST_KEY"); err == nil {
		t.Fatal("ambiguous credential accepted")
	}
	cfg := bootstrapConfig{
		Endpoint:     "http://localhost:9000",
		Owner:        "namespace/release",
		Region:       "us-east-1",
		Root:         "root-user",
		RootSecret:   "root-password",
		Issuer:       "issuer-user",
		IssuerSecret: "issuer-password",
	}
	for _, mutate := range []func(*bootstrapConfig){func(c *bootstrapConfig) { c.Issuer = c.Root }, func(c *bootstrapConfig) { c.Endpoint = "http://root:secret@localhost" }, func(c *bootstrapConfig) { c.Owner = "" }} {
		bad := cfg
		mutate(&bad)
		if bad.validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }),
	)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := waitReady(ctx, server.Client(), server.URL); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func waitGateway(ctx context.Context, id, endpoint string) error {
	client := &http.Client{Timeout: time.Second}
	for {
		if err := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", "kill -0 1").Run(); err != nil {
			return fmt.Errorf("JuiceFS container exited: %w", err)
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/minio/health/ready", nil)
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestJuiceFSBootstrapDocker(t *testing.T) {
	if os.Getenv("ARTIFACT_BOOTSTRAP_DOCKER_TEST") != "1" {
		t.Skip("set ARTIFACT_BOOTSTRAP_DOCKER_TEST=1 for isolated JuiceFS")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %s", args[0], out)
		}
		return strings.TrimSpace(string(out))
	}
	image := os.Getenv("ARTIFACT_TEST_JUICEFS_IMAGE")
	if image == "" {
		image = "artifact-juicefs:1.4.1-arm64"
	}
	id := docker(
		"run",
		"-d",
		"--rm",
		"-p",
		"127.0.0.1::9000",
		"-e",
		"MINIO_ROOT_USER=bootstrap-root",
		"-e",
		"MINIO_ROOT_PASSWORD=bootstrap-root-password",
		"--entrypoint",
		"/bin/sh",
		image,
		"-c",
		"mkdir -p /data && juicefs format --no-update --storage file --bucket /data/objects sqlite3:///data/meta.db bootstrap-test && exec juicefs gateway --multi-buckets --keep-etag --object-meta --no-usage-report sqlite3:///data/meta.db 0.0.0.0:9000",
	)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	cfg := bootstrapConfig{
		Endpoint:     "http://" + docker("port", id, "9000/tcp"),
		Owner:        "test/isolated",
		Region:       "us-east-1",
		Root:         "bootstrap-root",
		RootSecret:   "bootstrap-root-password",
		Issuer:       "bootstrap-issuer",
		IssuerSecret: "bootstrap-issuer-password",
	}
	if err := ensure(ctx, cfg); err != nil {
		t.Fatal("initial ensure:", err)
	}
	u, _ := url.Parse(cfg.Endpoint)
	admin, err := madmin.New(u.Host, cfg.Root, cfg.RootSecret, false)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := admin.GetUserInfo(ctx, cfg.Issuer)
	if err != nil {
		t.Fatal(err)
	}
	policyJSON, _ := admin.InfoCannedPolicy(ctx, initial.PolicyName)
	t.Logf(
		"created issuer status=%s policy=%q groups=%v document=%s",
		initial.Status,
		initial.PolicyName,
		initial.MemberOf,
		policyJSON,
	)
	if err := ensure(ctx, cfg); err != nil {
		t.Fatal("repeat ensure:", err)
	}
	repeated, err := admin.GetUserInfo(ctx, cfg.Issuer)
	if err != nil || !repeated.UpdatedAt.Equal(initial.UpdatedAt) {
		t.Fatal("repeat changed issuer", err)
	}
	wrong := cfg
	wrong.IssuerSecret = "different-valid-password"
	if ensure(ctx, wrong) == nil {
		t.Fatal("wrong existing secret accepted")
	}
	wrong = cfg
	wrong.Owner = "test/another-owner"
	if ensure(ctx, wrong) == nil {
		t.Fatal("foreign ownership accepted")
	}
	if err := ensure(ctx, cfg); err != nil {
		t.Fatal("credential mismatch changed issuer:", err)
	}
	if err := exec.CommandContext(ctx, "docker", "restart", id).Run(); err != nil {
		t.Fatal("restart gateway:", err)
	}
	portLine := docker("port", id, "9000/tcp")
	_, port, err := net.SplitHostPort(portLine)
	if err != nil {
		t.Fatal("parse restarted gateway port:", err)
	}
	cfg.Endpoint = "http://127.0.0.1:" + port
	if err := waitGateway(ctx, id, cfg.Endpoint); err != nil {
		t.Fatal("gateway restart:", err)
	}
	u, _ = url.Parse(cfg.Endpoint)
	admin, err = madmin.New(u.Host, cfg.Root, cfg.RootSecret, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensure(ctx, cfg); err != nil {
		t.Fatal("restart ensure:", err)
	}
	s3client := func(access, secret, token string) *s3.Client {
		return s3.New(
			s3.Options{
				Region:           cfg.Region,
				BaseEndpoint:     aws.String(cfg.Endpoint),
				UsePathStyle:     true,
				RetryMaxAttempts: 1,
				HTTPClient:       &http.Client{Timeout: 10 * time.Second},
				Credentials: aws.CredentialsProviderFunc(
					func(context.Context) (aws.Credentials, error) {
						return aws.Credentials{
							AccessKeyID:     access,
							SecretAccessKey: secret,
							SessionToken:    token,
						}, nil
					},
				),
			},
		)
	}
	root := s3client(cfg.Root, cfg.RootSecret, "")
	bucket := "tenant-t-012345678901234567890123"
	if _, err := root.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatal(err)
	}
	stsc := sts.New(
		sts.Options{
			Region:           cfg.Region,
			BaseEndpoint:     aws.String(cfg.Endpoint),
			RetryMaxAttempts: 1,
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: cfg.Issuer, SecretAccessKey: cfg.IssuerSecret}, nil
			}),
		},
	)
	session, err := stsc.AssumeRole(
		ctx,
		&sts.AssumeRoleInput{
			RoleArn:         aws.String(""),
			DurationSeconds: aws.Int32(900),
			RoleSessionName: aws.String("isolated-bootstrap-test"),
			Policy: aws.String(
				`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":["arn:aws:s3:::` + bucket + `/allowed"]}]}`,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	grant := s3client(
		aws.ToString(session.Credentials.AccessKeyId),
		aws.ToString(session.Credentials.SecretAccessKey),
		aws.ToString(session.Credentials.SessionToken),
	)
	payload := []byte("bootstrap byte fidelity\x00\xff")
	put := func(key string) error {
		_, err := grant.PutObject(
			ctx,
			&s3.PutObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(key),
				Body:   bytes.NewReader(payload),
			},
		)
		return err
	}
	if err := put("allowed"); err != nil {
		t.Fatal("allowed put", err)
	}
	var api smithy.APIError
	if err := put("outside"); !errors.As(err, &api) || api.ErrorCode() != "AccessDenied" {
		t.Fatal("cross-key expected AccessDenied", err)
	}
	response, err := grant.GetObject(
		ctx,
		&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("allowed")},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("byte fidelity failed", err)
	}
	_, err = grant.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("allowed")})
	if !errors.As(err, &api) || api.ErrorCode() != "AccessDenied" {
		t.Fatal("delete expected AccessDenied", err)
	}
	name, expected := cfg.policy()
	if err := admin.AddCannedPolicy(
		ctx,
		name,
		[]byte(strings.ReplaceAll(string(expected), "s3:GetObject", "s3:DeleteObject")),
	); err != nil {
		t.Fatal(err)
	}
	if ensure(ctx, cfg) == nil {
		t.Fatal("conflicting policy overwritten")
	}
	t.Log(
		"first/repeat/restart ensure, unchanged issuer, ownership/secret/policy rejection, ordinary STS, cross-key denial and bytes passed",
	)
}
