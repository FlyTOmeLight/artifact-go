package artifactv1

import "time"

type AliasPath struct {
	ArtifactPath
	Name string `json:"-" uri:"name" binding:"required,max=64"`
}

type PutAliasRequest struct {
	AliasPath
	VersionID        string `json:"versionID" binding:"required"`
	ExpectedRevision *int64 `json:"expectedRevision" binding:"required,gte=0"`
}

type DeleteAliasRequest struct {
	AliasPath
	ExpectedRevision *int64 `json:"-" form:"expectedRevision" binding:"required,gte=0"`
}

type GetAliasRequest struct {
	AliasPath
	IncludeDeleted bool `json:"-" form:"includeDeleted"`
}

type AliasResponse struct {
	ArtifactID string           `json:"artifactID"`
	Name       string           `json:"name"`
	Revision   int64            `json:"revision"`
	Target     *VersionIdentity `json:"target"`
	DeletedAt  *time.Time       `json:"deletedAt"`
}
