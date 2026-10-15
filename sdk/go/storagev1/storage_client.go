// Package storagev1 provides the storage HTTP client.
package storagev1

import (
	"context"
	"errors"
	"time"

	"github.com/FlyTOmeLight/server-go/sdk"
	"resty.dev/v3"
)

// ClientConfig configures the shared HTTP transport, without request credentials.
type ClientConfig struct {
	Endpoint string

	// Timeout is the request timeout; zero uses the framework default.
	Timeout time.Duration
}

// Client is safe for concurrent calls. Credentials belong to each request context.
type Client struct{ transport *sdk.Client }

func NewClient(config ClientConfig) (*Client, error) {
	if config.Timeout < 0 {
		return nil, errors.New("storage SDK timeout must not be negative")
	}
	transport, err := sdk.NewClientWithConfig(&sdk.Config{Endpoint: config.Endpoint, Retry: 0})
	if err != nil {
		return nil, err
	}
	if config.Timeout > 0 {
		transport.SetTimeout(config.Timeout)
	}
	return &Client{transport: transport}, nil
}

func (c *Client) Close() error { return c.transport.Close() }

// RequestIdentity carries this call's credential, selected scope, and correlation ID.
// Management calls need only the service credential; file calls use a verified user scope.
// The receiving service verifies the credential; the SDK does not authenticate it.
type RequestIdentity struct {
	BearerToken string
	TenantID    string
	ProjectID   string
	RequestID   string
}

type identityKey struct{}

// WithRequestIdentity snapshots credentials for one call without changing the client.
func WithRequestIdentity(ctx context.Context, identity RequestIdentity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

func (c *Client) request(ctx context.Context) *resty.Request {
	req := c.transport.R().SetContext(ctx).SetHeader("Accept", "application/json")
	if identity, ok := ctx.Value(identityKey{}).(RequestIdentity); ok {
		if identity.BearerToken != "" {
			req.SetAuthToken(identity.BearerToken)
		}
		if identity.TenantID != "" {
			req.SetHeader("X-Tenant-ID", identity.TenantID)
		}
		if identity.ProjectID != "" {
			req.SetHeader("X-Project-ID", identity.ProjectID)
		}
		if identity.RequestID != "" {
			req.SetHeader("X-Request-ID", identity.RequestID)
		}
	}
	return req
}
