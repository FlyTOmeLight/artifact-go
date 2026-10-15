package storagev1

import "time"

// GrantScope selects one preset or explicit native S3 actions.
type GrantScope struct {
	Preset  string   `json:"preset,omitempty"`
	Actions []string `json:"actions,omitempty"`
}

// CreateGrantRequest authorizes all files with one temporary credential.
type CreateGrantRequest struct {
	FileIDs    []string   `json:"fileIDs" binding:"required,min=1,max=1000"`
	Scope      GrantScope `json:"scope"`
	TTLSeconds *int64     `json:"ttlSeconds,omitempty"`
}

// GrantCredentials contains short-lived S3 credentials. Do not log these values.
type GrantCredentials struct {
	AccessKeyID     string `json:"accessKeyID"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
}

// GrantFile is the exact object location covered by the credential.
type GrantFile struct {
	FileID    string `json:"fileID"`
	Bucket    string `json:"bucket"`
	ObjectKey string `json:"objectKey"`
}

// GrantResponse covers every requested file, preserving the request's file order.
type GrantResponse struct {
	Endpoint       string           `json:"endpoint"`
	Region         string           `json:"region"`
	ForcePathStyle bool             `json:"forcePathStyle"`
	ExpiresAt      time.Time        `json:"expiresAt"`
	Actions        []string         `json:"actions"`
	Credentials    GrantCredentials `json:"credentials"`
	Files          []GrantFile      `json:"files"`
}
