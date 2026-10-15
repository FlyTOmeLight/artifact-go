package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	serrors "github.com/FlyTOmeLight/server-go/errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// GrantIssuerConfig is a dedicated ordinary IAM identity, separate from bucket administration.
type GrantIssuerConfig struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	ExpiresAt       time.Time
}

func (g GrantIssuerConfig) validate(adminAccessKey string) error {
	u, err := url.Parse(g.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		u.RawQuery != "" ||
		u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return errors.New("STS endpoint must be an HTTP(S) origin")
	}
	if g.AccessKeyID == "" || g.SecretAccessKey == "" || g.AccessKeyID == adminAccessKey {
		return errors.New("STS requires a dedicated ordinary IAM identity distinct from bucket administration")
	}
	if g.SessionToken != "" && g.ExpiresAt.IsZero() {
		return errors.New("temporary STS issuer credentials require expiresAt")
	}
	if !g.ExpiresAt.IsZero() && !g.ExpiresAt.After(time.Now()) {
		return errors.New("STS issuer credentials have expired")
	}
	return nil
}

type grantClient struct {
	sts       *sts.Client
	expiresAt time.Time
}

func newGrantClient(cfg Config) *grantClient {
	if cfg.GrantIssuer == nil {
		return nil
	}
	g := *cfg.GrantIssuer
	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{
			AccessKeyID:     g.AccessKeyID,
			SecretAccessKey: g.SecretAccessKey,
			SessionToken:    g.SessionToken,
			Source:          "artifact-storage-grant-issuer",
			CanExpire:       !g.ExpiresAt.IsZero(),
			Expires:         g.ExpiresAt,
		}, nil
	})
	return &grantClient{
		sts: sts.New(
			sts.Options{
				Region:           cfg.Region,
				BaseEndpoint:     aws.String(strings.TrimSuffix(g.Endpoint, "/")),
				Credentials:      credentials,
				HTTPClient:       &http.Client{Timeout: 5 * time.Second},
				RetryMaxAttempts: 1,
			},
		),
		expiresAt: g.ExpiresAt,
	}
}

type GrantConnection struct {
	Endpoint, Region string
	ForcePathStyle   bool
}

func (c *Client) GrantConnection() GrantConnection { return c.connection }

func (c *Client) GrantEnabled() bool { return c.grant != nil }

func (c *Client) GrantIssuerExpiry() time.Time {
	if c.grant == nil {
		return time.Time{}
	}
	return c.grant.expiresAt
}

type GrantRequest struct {
	Bucket        string
	Keys, Actions []string
	Deadline      time.Time
}

type GrantCredentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	ExpiresAt                                  time.Time
}

func grantUnavailable(cause error) error {
	return &backendError{
		public: serrors.NewError(503, "StorageUnavailable", "storage credential issuance failed"),
		cause:  cause,
	}
}

// CreateGrant issues one exact-key session policy. Deadline is an absolute upper
// bound reserved by the caller before this external operation, never a fresh TTL.
func (c *Client) CreateGrant(ctx context.Context, request GrantRequest) (GrantCredentials, error) {
	var empty GrantCredentials
	if c.grant == nil {
		return empty, grantUnavailable(errors.New("grant issuer is not configured"))
	}
	if request.Bucket == "" || strings.ContainsAny(request.Bucket, "*?/") || len(request.Keys) == 0 ||
		len(request.Actions) == 0 {
		return empty, serrors.NewError(400, "InvalidGrantScope", "exact resources and actions are required")
	}
	resources := make([]string, len(request.Keys))
	for i, key := range request.Keys {
		if key == "" || strings.ContainsAny(key, "*?") {
			return empty, serrors.NewError(400, "InvalidGrantScope", "exact object keys are required")
		}
		resources[i] = "arn:aws:s3:::" + request.Bucket + "/" + key
	}
	for _, action := range request.Actions {
		switch action {
		case "s3:GetObject", "s3:PutObject", "s3:ListMultipartUploadParts", "s3:AbortMultipartUpload":
		default:
			return empty, serrors.NewError(400, "InvalidGrantScope", "unsupported S3 action")
		}
	}
	policy, _ := json.Marshal(struct {
		Version   string
		Statement []any
	}{Version: "2012-10-17", Statement: []any{struct {
		Effect           string
		Action, Resource []string
	}{Effect: "Allow", Action: request.Actions, Resource: resources}}})
	if len(policy) > 2048 {
		return empty, serrors.NewError(
			400,
			"GrantScopeTooLarge",
			"session policy exceeds 2048 bytes; split the file batch",
		)
	}
	deadline := request.Deadline
	if expiry := c.grant.expiresAt; !expiry.IsZero() && expiry.Before(deadline) {
		deadline = expiry
	}
	duration := int64(time.Until(deadline).Seconds()) - 5
	if duration < 900 {
		return empty, serrors.NewError(
			400,
			"GrantTTLTooShort",
			"remaining grant lifetime must allow at least 900 seconds plus issuance budget",
		)
	}
	if duration > int64(1<<31-1) {
		return empty, serrors.NewError(400, "InvalidParameter", "grant lifetime exceeds STS duration range")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// JuiceFS authenticates the ordinary IAM issuer directly and ignores role ARN.
	// Non-nil empty ARN satisfies the official SDK validator without inventing an AWS role.
	output, err := c.grant.sts.AssumeRole(
		ctx,
		&sts.AssumeRoleInput{
			RoleArn:         aws.String(""),
			RoleSessionName: aws.String("artifact-storage"),
			DurationSeconds: aws.Int32(int32(duration)),
			Policy:          aws.String(string(policy)),
		},
	)
	if err != nil {
		return empty, grantUnavailable(err)
	}
	if output == nil || output.Credentials == nil {
		return empty, grantUnavailable(errors.New("STS omitted credentials"))
	}
	credentials := output.Credentials
	if credentials.AccessKeyId == nil || *credentials.AccessKeyId == "" || credentials.SecretAccessKey == nil ||
		*credentials.SecretAccessKey == "" ||
		credentials.SessionToken == nil ||
		*credentials.SessionToken == "" ||
		credentials.Expiration == nil ||
		!credentials.Expiration.After(time.Now()) ||
		credentials.Expiration.After(deadline) {
		return empty, grantUnavailable(errors.New("STS returned missing credentials or invalid expiry"))
	}
	return GrantCredentials{
		AccessKeyID:     *credentials.AccessKeyId,
		SecretAccessKey: *credentials.SecretAccessKey,
		SessionToken:    *credentials.SessionToken,
		ExpiresAt:       *credentials.Expiration,
	}, nil
}
