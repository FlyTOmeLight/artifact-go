package storagev1

// CreateTenantBucketRequest identifies the tenant whose fixed bucket is prepared.
type CreateTenantBucketRequest struct {
	TenantID string `json:"tenantID" binding:"required"`
}

// TenantBucketResponse identifies the prepared storage location without credentials.
type TenantBucketResponse struct {
	TenantID  string `json:"tenantID"`
	BackendID string `json:"backendID"`
	Bucket    string `json:"bucket"`
}
