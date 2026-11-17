package artifactv1

import "context"

type RequestIdentity struct {
	BearerToken string
	Session     string
	CSRFToken   string
	Origin      string
	TenantID    string
	ProjectID   *string

	// PropagatedUserID carries the target user for trusted internal propagation;
	// BearerToken supplies the calling service's own credential.
	PropagatedUserID string

	ResourceUserID *string
	RequestID      string
}

type identityKey struct{}

// WithRequestIdentity snapshots the request credentials and scope selection.
// These values are authenticated by the receiving service, not by the SDK.
// Session, CSRFToken and Origin carry the original call's browser credentials.
// BearerToken and PropagatedUserID carry service proof and an impersonation
// target; mixed proofs are sent unchanged for the server to reject.
func WithRequestIdentity(ctx context.Context, identity RequestIdentity) context.Context {
	if identity.ProjectID != nil {
		project := *identity.ProjectID
		identity.ProjectID = &project
	}
	if identity.ResourceUserID != nil {
		user := *identity.ResourceUserID
		identity.ResourceUserID = &user
	}
	return context.WithValue(ctx, identityKey{}, identity)
}
