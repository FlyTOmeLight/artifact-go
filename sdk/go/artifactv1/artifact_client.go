package artifactv1

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/FlyTOmeLight/server-go/sdk"
	"resty.dev/v3"
)

type ClientConfig struct {
	Endpoint string

	// Timeout is the request timeout; zero uses the framework default.
	Timeout time.Duration
}

// Client is safe for concurrent calls. Identity belongs to each request context.
type Client struct{ transport *sdk.Client }

func NewClient(config ClientConfig) (*Client, error) {
	if config.Timeout < 0 {
		return nil, errors.New("artifact SDK timeout must not be negative")
	}
	transport, err := sdk.NewClientWithConfig(&sdk.Config{Endpoint: config.Endpoint, Retry: 0})
	if err != nil {
		return nil, err
	}
	// Credentials belong to one call, including when a server sets cookies or redirects.
	transport.SetCookieJar(nil)
	transport.Client.Client().CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("artifact SDK refuses HTTP redirects")
	}
	if config.Timeout > 0 {
		transport.SetTimeout(config.Timeout)
	}
	return &Client{transport: transport}, nil
}

func (c *Client) Close() error { return c.transport.Close() }

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
		if identity.ProjectID != nil && *identity.ProjectID != "" {
			req.SetHeader("X-Project-ID", *identity.ProjectID)
		}
		if identity.RequestID != "" {
			req.SetHeader("X-Request-ID", identity.RequestID)
		}
	}
	return req
}
