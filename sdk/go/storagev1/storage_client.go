// Package storagev1 provides the storage HTTP client.
package storagev1

import (
	"context"
	"errors"
	"net/http"
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
	// Credentials belong to one call, including when a server sets cookies or redirects.
	transport.SetCookieJar(nil)
	transport.Client.Client().CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("storage SDK refuses HTTP redirects")
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
	BearerToken    string
	Session        string
	CSRFToken      string
	Origin         string
	TenantID       string
	ProjectID      string
	ResourceUserID *string
	RequestID      string
}

type identityKey struct{}

// WithRequestIdentity snapshots credentials for one call without changing the client.
// Session, CSRFToken and Origin carry the original call's browser credentials.
func WithRequestIdentity(ctx context.Context, identity RequestIdentity) context.Context {
	if identity.ResourceUserID != nil {
		user := *identity.ResourceUserID
		identity.ResourceUserID = &user
	}
	return context.WithValue(ctx, identityKey{}, identity)
}

func (c *Client) request(ctx context.Context) *resty.Request {
	req := c.transport.R().SetContext(ctx).SetHeader("Accept", "application/json")
	if identity, ok := ctx.Value(identityKey{}).(RequestIdentity); ok {
		if identity.Session != "" {
			req.SetCookies([]*http.Cookie{{Name: "forge_session", Value: identity.Session}})
		}
		if identity.CSRFToken != "" {
			req.SetHeader("X-CSRF-Token", identity.CSRFToken)
		}
		if identity.Origin != "" {
			req.SetHeader("Origin", identity.Origin)
		}
		if identity.BearerToken != "" {
			req.SetAuthToken(identity.BearerToken)
		}
		if identity.TenantID != "" {
			req.SetHeader("X-Tenant-ID", identity.TenantID)
		}
		if identity.ProjectID != "" {
			req.SetHeader("X-Project-ID", identity.ProjectID)
		}
		if identity.ResourceUserID != nil {
			req.SetHeader("X-Resource-User-ID", *identity.ResourceUserID)
		}
		if identity.RequestID != "" {
			req.SetHeader("X-Request-ID", identity.RequestID)
		}
	}
	return req
}
