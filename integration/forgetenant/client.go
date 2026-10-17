// Package forgetenant connects artifact identity to Forge's existing session and
// tenant-membership checks. It does not create sessions or grant project roles.
package forgetenant

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/artifact-go/service"
)

type Config struct {
	Endpoint     string
	ServiceToken string
	Timeout      time.Duration
}

type Client struct {
	endpoint, token string
	http            *http.Client
}

// New owns its HTTP transport. Close it after all requests have finished.
// This wire adapter uses the existing Forge protocol without an unpublished SDK
// dependency or a permanent module replacement.
func New(config Config) (*Client, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return nil, errors.New("Forge tenant endpoint must be an HTTP(S) origin")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("Forge tenant endpoint requires HTTPS outside loopback")
	}
	if !validToken(config.ServiceToken) {
		return nil, errors.New("Forge tenant service token requires at least 32 characters without whitespace")
	}
	if config.Timeout < 0 {
		return nil, errors.New("Forge tenant timeout must not be negative")
	}
	if config.Timeout == 0 {
		config.Timeout = 15 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{
		endpoint: strings.TrimRight(config.Endpoint, "/") + "/internal/v1/scope/resolve",
		token:    config.ServiceToken,
		http: &http.Client{
			Transport: transport,
			Timeout:   config.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("Forge tenant scope resolver refuses HTTP redirects")
			},
		},
	}, nil
}

func (c *Client) Close() error { c.http.CloseIdleConnections(); return nil }

// Resolve preserves the caller's method, origin and CSRF. Scope headers select a
// scope; Forge must authenticate its user and confirm membership and ownership.
// Only active ownership is supported by this integration.
func (c *Client) Resolve(r *http.Request) (service.RequestIdentity, error) {
	var empty service.RequestIdentity
	cookie, err := r.Cookie("forge_session")
	if err != nil || cookie.Value == "" {
		return empty, serrors.NewError(http.StatusUnauthorized, "AUTH_REQUIRED", "请先登录")
	}
	tenantID, project := r.Header.Get("X-Tenant-ID"), r.Header.Get("X-Project-ID")
	if strings.TrimSpace(tenantID) == "" || tenantID != strings.TrimSpace(tenantID) ||
		project != strings.TrimSpace(project) {
		return empty, serrors.NewError(http.StatusBadRequest, "InvalidScope", "请选择有效的租户或项目范围")
	}
	var projectID *string
	if project != "" {
		projectID = &project
	}
	input := struct {
		Session   string  `json:"session"`
		Method    string  `json:"method"`
		Origin    string  `json:"origin"`
		CSRF      string  `json:"csrf"`
		TenantID  string  `json:"tenant_id"`
		ProjectID *string `json:"project_id"`
	}{Session: cookie.Value, Method: r.Method, Origin: r.Header.Get("Origin"), CSRF: r.Header.Get("X-CSRF-Token"), TenantID: tenantID, ProjectID: projectID}
	body, err := json.Marshal(input)
	if err != nil {
		return empty, unavailable()
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return empty, unavailable()
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			return empty, r.Context().Err()
		}
		return empty, unavailable()
	}
	defer response.Body.Close()
	const maxResponseBytes = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if r.Context().Err() != nil {
		return empty, r.Context().Err()
	}
	if err != nil || len(raw) > maxResponseBytes {
		return empty, unavailable()
	}
	if response.StatusCode != http.StatusOK {
		return empty, responseError(response.StatusCode, raw)
	}
	var result struct {
		UserID         string          `json:"user_id"`
		TenantID       string          `json:"tenant_id"`
		ProjectID      json.RawMessage `json:"project_id"`
		OwnershipState string          `json:"ownership_state"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil {
		return empty, unavailable()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return empty, unavailable()
	}
	var resolvedProject *string
	if len(result.ProjectID) == 0 || json.Unmarshal(result.ProjectID, &resolvedProject) != nil ||
		strings.TrimSpace(result.UserID) == "" || result.UserID != strings.TrimSpace(result.UserID) ||
		result.TenantID != tenantID || result.OwnershipState != "active" ||
		(projectID == nil) != (resolvedProject == nil) ||
		(projectID != nil && *projectID != *resolvedProject) {
		return empty, unavailable()
	}
	var resourceUser *string
	if values, present := r.Header[http.CanonicalHeaderKey("X-Resource-User-ID")]; present {
		if len(values) != 1 || values[0] == "" {
			return empty, serrors.NewError(400, "InvalidScope", "resource user must be a nonempty ID")
		}
		if values[0] != result.UserID {
			return empty, serrors.NewError(
				403,
				"ScopeAccessDenied",
				"private resources belong to another user",
			)
		}
		selected := values[0]
		resourceUser = &selected
	}
	return service.RequestIdentity{
		ResourceUserID: resourceUser,
		UserID:         result.UserID,
		TenantID:       tenantID,
		ProjectID:      resolvedProject,
		OwnershipState: service.OwnershipActive,
	}, nil
}

func unavailable() error {
	return serrors.NewError(http.StatusServiceUnavailable, "TenantIdentityUnavailable", "租户身份校验暂不可用")
}

func responseError(status int, raw []byte) error {
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return unavailable()
	}
	switch {
	case status == http.StatusUnauthorized && result.Error.Code == "AUTH_REQUIRED":
		return serrors.NewError(status, "AUTH_REQUIRED", "请先登录")
	case status == http.StatusForbidden && result.Error.Code == "CSRF_REJECTED":
		return serrors.NewError(status, "CSRF_REJECTED", "请求来源校验失败")
	case status == http.StatusForbidden && result.Error.Code == "SCOPE_ACCESS_DENIED":
		return serrors.NewError(status, "SCOPE_ACCESS_DENIED", "无权访问所选租户或项目")
	case status == http.StatusForbidden && result.Error.Code == "SCOPE_INACTIVE":
		return serrors.NewError(status, "SCOPE_INACTIVE", "所选租户或项目已停用")
	case status == http.StatusNotFound && result.Error.Code == "SCOPE_NOT_FOUND":
		return serrors.NewError(status, "SCOPE_NOT_FOUND", "所选租户或项目不存在")
	default:
		return unavailable()
	}
}

func validToken(token string) bool {
	return len(token) >= 32 && strings.IndexFunc(token, unicode.IsSpace) < 0
}

// NewManagementVerifier accepts only dedicated bucket-management Bearer tokens.
// Tokens must be separate from user sessions and the outbound Tenant API token.
// Keep both old and new tokens during rotation, then reconstruct without the old.
func NewManagementVerifier(tokens []string) (service.TenantServiceVerifier, error) {
	if len(tokens) == 0 {
		return nil, errors.New("at least one bucket-management token is required")
	}
	seen := make(map[string]bool)
	copied := append([]string(nil), tokens...)
	for _, token := range copied {
		if !validToken(token) || seen[token] {
			return nil, errors.New("invalid or duplicate bucket-management token")
		}
		seen[token] = true
	}
	return func(r *http.Request) error {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			token := strings.TrimPrefix(auth, "Bearer ")
			matched := 0
			for _, allowed := range copied {
				matched |= subtle.ConstantTimeCompare([]byte(token), []byte(allowed))
			}
			if matched == 1 {
				return nil
			}
		}
		return serrors.NewError(http.StatusUnauthorized, "TenantServiceUnauthorized", "需要租户管理服务凭据")
	}, nil
}
