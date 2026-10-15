package artifactv1

import "context"

type RequestIdentity struct {
	BearerToken string
	TenantID    string
	ProjectID   *string
	RequestID   string
}

type identityKey struct{}

// WithRequestIdentity snapshots the request credentials and scope selection.
// These values are authenticated by the receiving service, not by the SDK.
func WithRequestIdentity(ctx context.Context, identity RequestIdentity) context.Context {
	if identity.ProjectID != nil {
		project := *identity.ProjectID
		identity.ProjectID = &project
	}
	return context.WithValue(ctx, identityKey{}, identity)
}
