package artifactv1

import frameworkerrors "github.com/FlyTOmeLight/server-go/errors"

// ErrorResponse retains the standard framework HTTP error metadata.
type ErrorResponse = frameworkerrors.Error

const (
	CodeInvalidURI                  = "InvalidURI"
	CodeInvalidResolveRequest       = "InvalidResolveRequest"
	CodeDraftDeleted                = "DraftDeleted"
	CodeInvalidDraftRequest         = "InvalidDraftRequest"
	CodeRequestInProgress           = "RequestInProgress"
	CodeRetentionBlocked            = "RetentionBlocked"
	CodeInvalidRetentionRequest     = "InvalidRetentionRequest"
	CodeVersionProtected            = "VersionProtected"
	CodeVersionReferenced           = "VersionReferenced"
	CodeRevisionConflict            = "RevisionConflict"
	CodeRevisionExhausted           = "RevisionExhausted"
	CodeInvalidAliasRequest         = "InvalidAliasRequest"
	CodeOperationOutcomeUnknown     = "OperationOutcomeUnknown"
	CodeInvalidVersionRequest       = "InvalidVersionRequest"
	CodeIdempotentParameterMismatch = "IdempotentParameterMismatch"
	CodeResourceNotFound            = "ResourceNotFound"
	CodeVersionDeleted              = "VersionDeleted"
	CodeArtifactArchived            = "ArtifactArchived"
	CodeVersionNumberExhausted      = "VersionNumberExhausted"
	CodePayloadTooLarge             = "PayloadTooLarge"
	CodeAnnotationsTooLarge         = "AnnotationsTooLarge"
	CodeArtifactNameConflict        = "ArtifactNameConflict"
	CodeArtifactNotFound            = "ArtifactNotFound"
	CodeInvalidCursor               = "InvalidCursor"
	CodeInvalidParameter            = "InvalidParameter"
	CodeScopeInactive               = "ScopeInactive"
	CodeScopeMismatch               = "ScopeMismatch"
	CodeUnauthorized                = "Unauthorized"
	CodeRequestBodyTooLarge         = "RequestBodyTooLarge"
)
