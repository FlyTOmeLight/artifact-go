// Command storage-bootstrap ensures the ordinary IAM issuer used for storage STS grants.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	madmin "github.com/minio/madmin-go/v2"
)

type bootstrapConfig struct {
	Endpoint, Owner, Region                string
	Root, RootSecret, Issuer, IssuerSecret string
}

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
	flags := flag.NewFlagSet("artifact-storage-bootstrap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfg bootstrapConfig
	flags.StringVar(&cfg.Endpoint, "endpoint", "", "required JuiceFS HTTP(S) origin")
	flags.StringVar(&cfg.Owner, "owner", "", "required stable deployment identity (namespace/release)")
	flags.StringVar(&cfg.Region, "region", "us-east-1", "S3 signing region")
	timeout := flags.Duration("timeout", 2*time.Minute, "total readiness and IAM initialization timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *timeout <= 0 {
		return errors.New("positive timeout required; positional arguments are not supported")
	}
	for _, entry := range []struct {
		name  string
		value *string
	}{
		{name: "S3_ACCESS_KEY_ID", value: &cfg.Root},
		{name: "S3_SECRET_ACCESS_KEY", value: &cfg.RootSecret},
		{name: "STS_ACCESS_KEY_ID", value: &cfg.Issuer},
		{name: "STS_SECRET_ACCESS_KEY", value: &cfg.IssuerSecret},
	} {
		value, err := credential(entry.name)
		if err != nil {
			return err
		}
		*entry.value = value
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := ensure(ctx, cfg); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, `{"status":"ready"}`)
	return err
}

func credential(name string) (string, error) {
	value, set := os.LookupEnv(name)
	path, fileSet := os.LookupEnv(name + "_FILE")
	if set && fileSet {
		return "", fmt.Errorf("set exactly one of %s and %s_FILE", name, name)
	}
	if fileSet {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("cannot read %s_FILE", name)
		}
		value = strings.TrimRight(string(data), "\r\n")
	}
	if value == "" {
		return "", fmt.Errorf("%s or %s_FILE is required", name, name)
	}
	return value, nil
}

func (c bootstrapConfig) validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return errors.New("endpoint must be an HTTP(S) origin without credentials, query or path")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,127}$`).MatchString(c.Owner) || c.Region == "" {
		return errors.New("owner must be a stable deployment identity, and region is required")
	}
	if c.Root == "" || c.RootSecret == "" || len(c.Issuer) < 3 || len(c.IssuerSecret) < 8 {
		return errors.New("root credentials and issuer credentials (access key >=3, secret >=8 bytes) required")
	}
	if c.Root == c.Issuer {
		return errors.New("STS issuer must differ from the root account")
	}
	return nil
}

func (c bootstrapConfig) policy() (string, []byte) {
	digest := sha256.Sum256([]byte(c.Owner))
	name := "artifact-sts-" + hex.EncodeToString(digest[:16])
	document, _ := json.Marshal(map[string]any{
		"Version": "2012-10-17", "ID": "artifact-storage-bootstrap:" + c.Owner + ":" + c.Issuer,
		"Statement": []any{
			map[string]any{
				"Effect": "Allow",
				"Action": []string{
					"s3:GetObject",
					"s3:PutObject",
					"s3:AbortMultipartUpload",
					"s3:ListMultipartUploadParts",
				},
				"Resource": []string{"arn:aws:s3:::tenant-t-*/*"},
			},
		},
	})
	return name, document
}

func ensure(ctx context.Context, cfg bootstrapConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	u, _ := url.Parse(cfg.Endpoint)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if err := waitReady(ctx, httpClient, strings.TrimRight(cfg.Endpoint, "/")); err != nil {
		return err
	}
	admin, err := madmin.New(u.Host, cfg.Root, cfg.RootSecret, u.Scheme == "https")
	if err != nil {
		return errors.New("cannot construct IAM admin client")
	}
	admin.SetCustomTransport(transport)
	policyName, expected := cfg.policy()
	user, userErr := admin.GetUserInfo(ctx, cfg.Issuer)
	missingUser := madmin.ToErrorResponse(userErr).Code == "XMinioAdminNoSuchUser"
	if userErr != nil && !missingUser {
		return iamError(ctx, "inspect issuer", userErr)
	}
	if !missingUser &&
		((user.PolicyName != "" && user.PolicyName != policyName) || user.Status != madmin.AccountEnabled || len(user.MemberOf) != 0) {
		return errors.New("existing issuer ownership, policy or status differs; refusing to overwrite")
	}
	actual, policyErr := admin.InfoCannedPolicy(ctx, policyName)
	missingPolicy := madmin.ToErrorResponse(policyErr).Code == "XMinioAdminNoSuchPolicy"
	if policyErr != nil && !missingPolicy {
		return iamError(ctx, "inspect issuer policy", policyErr)
	}
	if !missingPolicy && !samePolicy(actual, expected) {
		return errors.New("existing issuer policy ownership or permissions differ; refusing to overwrite")
	}
	if !missingUser && missingPolicy {
		return errors.New("existing issuer policy is missing; refusing to repair an unverified account")
	}
	if missingPolicy {
		if err := admin.AddCannedPolicy(ctx, policyName, expected); err != nil {
			return iamError(ctx, "create issuer policy", err)
		}
	}
	if missingUser {
		// JuiceFS ignores SetUserReq.Policy. The separately persisted owner policy
		// and matching issuer credentials permit recovery before SetPolicy below.
		if err := admin.SetUserReq(
			ctx,
			cfg.Issuer,
			madmin.AddOrUpdateUserReq{
				SecretKey: cfg.IssuerSecret,
				Policy:    policyName,
				Status:    madmin.AccountEnabled,
			},
		); err != nil {
			return iamError(ctx, "create issuer", err)
		}
	}
	// Prove the supplied ordinary issuer secret still works without resetting it.
	// The probe session grants only a nonexistent exact key and is never exposed.
	client := sts.New(sts.Options{
		Region: cfg.Region, BaseEndpoint: aws.String(cfg.Endpoint), HTTPClient: httpClient, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: cfg.Issuer, SecretAccessKey: cfg.IssuerSecret}, nil
		}),
	})
	_, err = client.AssumeRole(
		ctx,
		&sts.AssumeRoleInput{
			RoleArn:         aws.String(""),
			DurationSeconds: aws.Int32(900),
			RoleSessionName: aws.String("artifact-bootstrap-check"),
			Policy: aws.String(
				`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::artifact-bootstrap-no-access/probe"]}]}`,
			),
		},
	)
	if err != nil {
		return iamError(ctx, "verify ordinary issuer credentials", err)
	}
	if missingUser || user.PolicyName == "" {
		if err := admin.SetPolicy(ctx, policyName, cfg.Issuer, false); err != nil {
			return iamError(ctx, "attach issuer policy", err)
		}
	}
	return nil
}

func waitReady(ctx context.Context, client *http.Client, endpoint string) error {
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/minio/health/ready", nil)
		if err != nil {
			return errors.New("cannot construct readiness request")
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for JuiceFS: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func iamError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	}
	// Backend messages can echo supplied credentials; return only the operation.
	_ = err
	return fmt.Errorf("%s failed", operation)
}

func samePolicy(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(normalizePolicy(left), normalizePolicy(right))
}

func normalizePolicy(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			v[key] = normalizePolicy(child)
		}
	case []any:
		for i, child := range v {
			v[i] = normalizePolicy(child)
		}
		sort.Slice(v, func(i, j int) bool {
			a, _ := json.Marshal(v[i])
			b, _ := json.Marshal(v[j])
			return string(a) < string(b)
		})
	}
	return value
}
