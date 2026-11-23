package forgetenant

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/FlyTOmeLight/server-go/constants"
	serrors "github.com/FlyTOmeLight/server-go/errors"

	"github.com/FlyTOmeLight/artifact-go/service"
)

func (c *Client) resolvePropagation(
	r *http.Request,
	tenantID string,
	projectID *string,
) (service.RequestIdentity, error) {
	var empty service.RequestIdentity
	if len(c.allowedPropagationServices) == 0 {
		return empty, serrors.NewError(403, "ScopeAccessDenied", "internal propagation is not permitted")
	}
	if forgeSession(r) != "" {
		return empty, serrors.NewError(
			400,
			"AMBIGUOUS_IDENTITY_PROOF",
			"internal propagation cannot carry a session",
		)
	}
	token, hasBearer := bearerCredential(r.Header.Get("Authorization"))
	if !hasBearer || token == "" || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return empty, serrors.NewError(401, "SERVICE_AUTH_REQUIRED", "internal service credential required")
	}
	values := make([]string, 0, 3)
	for _, header := range []string{
		constants.HeaderImpersonateTenantID,
		constants.HeaderImpersonateUserID,
		constants.HeaderImpersonateProjectID,
	} {
		selected := r.Header.Values(header)
		if header == constants.HeaderImpersonateProjectID && len(selected) == 0 {
			values = append(values, "")
			continue
		}
		if len(selected) != 1 || selected[0] == "" || strings.IndexFunc(selected[0], unicode.IsSpace) >= 0 {
			return empty, invalidScope()
		}
		values = append(values, selected[0])
	}
	project := ""
	if projectID != nil {
		project = *projectID
	}
	if values[0] != tenantID || values[2] != project {
		return empty, serrors.NewError(
			403,
			"ScopeAccessDenied",
			"propagated identity does not match selected scope",
		)
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.serviceEndpoint, nil)
	if err != nil {
		return empty, unavailable()
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
	if response.StatusCode == http.StatusUnauthorized {
		return empty, serrors.NewError(401, "SERVICE_AUTH_REQUIRED", "unknown internal service key")
	}
	if response.StatusCode != http.StatusOK {
		return empty, unavailable()
	}
	var result struct {
		Service string `json:"service"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&result) != nil || result.Service == "" ||
		strings.IndexFunc(result.Service, unicode.IsSpace) >= 0 {
		return empty, unavailable()
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return empty, unavailable()
	}
	if !c.allowedPropagationServices[result.Service] {
		return empty, serrors.NewError(403, "ScopeAccessDenied", "internal service cannot propagate identity")
	}
	identity, err := resolvedIdentity(r, values[1], tenantID, projectID)
	if err != nil {
		return empty, err
	}
	identity.PropagatingService = result.Service
	return identity, nil
}
