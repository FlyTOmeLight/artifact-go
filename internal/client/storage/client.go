package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Config identifies the single provisioned S3 backend. Credentials never leave this adapter.
type Config struct {
	BackendID       string
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	UsePathStyle    bool
	GrantIssuer     *GrantIssuerConfig
}

type Client struct {
	s3         *s3.Client
	backendID  string
	region     string
	grant      *grantClient
	connection GrantConnection
}

func (cfg Config) Validate() error {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.User != nil ||
		endpoint.RawQuery != "" ||
		endpoint.Fragment != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("storage endpoint must be an HTTP(S) origin without credentials, query or path")
	}
	if strings.TrimSpace(cfg.BackendID) == "" || strings.TrimSpace(cfg.Region) == "" || cfg.AccessKeyID == "" ||
		cfg.SecretAccessKey == "" {
		return errors.New("storage backendID, region and credentials are required")
	}
	if cfg.GrantIssuer != nil {
		if err := cfg.GrantIssuer.validate(cfg.AccessKeyID); err != nil {
			return err
		}
	}
	return nil
}

func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	credentials := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{
			AccessKeyID:     cfg.AccessKeyID,
			SecretAccessKey: cfg.SecretAccessKey,
			SessionToken:    cfg.SessionToken,
			Source:          "artifact-storage-config",
		}, nil
	})
	backend := s3.New(
		s3.Options{
			Region:           cfg.Region,
			BaseEndpoint:     aws.String(strings.TrimSuffix(cfg.Endpoint, "/")),
			UsePathStyle:     cfg.UsePathStyle,
			Credentials:      credentials,
			HTTPClient:       &http.Client{Timeout: 5 * time.Second},
			RetryMaxAttempts: 1,
		},
	)
	return &Client{
		s3:        backend,
		backendID: cfg.BackendID,
		region:    cfg.Region,
		grant:     newGrantClient(cfg),
		connection: GrantConnection{
			Endpoint:       strings.TrimSuffix(cfg.Endpoint, "/"),
			Region:         cfg.Region,
			ForcePathStyle: cfg.UsePathStyle,
		},
	}, nil
}

func (c *Client) BackendID() string { return c.backendID }
