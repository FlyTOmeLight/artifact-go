package artifactv1

// ResolveRequest locates a persistent artifact URI within the caller's verified scope.
type ResolveRequest struct {
	URI string `json:"uri" binding:"required"`
}

// ResolveResponse identifies the collection and, when selected, a ready version
// and file manifest entry. It contains no storage grants or file bytes.
type ResolveResponse struct {
	Artifact ArtifactResponse `json:"artifact"`
	Version  *VersionResponse `json:"version"`
	File     *FileResponse    `json:"file"`
}
