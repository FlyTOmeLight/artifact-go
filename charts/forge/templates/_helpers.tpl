{{/*
Reject release/namespace combinations whose derived names would exceed the
Kubernetes 63-character resource name limit or the tenant CLI's 2-63 character
stable-name rule. Nothing here is truncated: the short names stay unchanged and
an invalid combination fails before any resource is created.

Longest derived resource names (release length R):
  R-artifact-integration-nginx   R+27  -> R <= 36
  R-external-bootstrap-<rev>     R+20+revision digits (R=36 allows 7 digits)
Longest derived CLI identities (namespace N, release R):
  N-R-management                 N+R+12 -> N+R <= 51
  N-R-console / N-R-tenant       shorter
  N-R (StorageConsumerID, <=64)  shorter
*/}}
{{- define "forge.validateNames" -}}
{{- $release := .Release.Name -}}
{{- $namespace := .Release.Namespace -}}
{{- if not (regexMatch "^[a-z]([-a-z0-9]*[a-z0-9])?$" $release) -}}
{{- fail (printf "forge: release name %q must start with a lowercase letter and contain only lowercase letters, digits and hyphens, without a trailing hyphen" $release) -}}
{{- end -}}
{{- if gt (len $release) 36 -}}
{{- fail (printf "forge: release name %q is %d characters; at most 36 keeps derived resource names within 63 characters (longest: %s-artifact-integration-nginx)" $release (len $release) $release) -}}
{{- end -}}
{{- if lt (len $namespace) 2 -}}
{{- fail (printf "forge: namespace %q must be at least 2 characters because bootstrap identity names must be 2-63 characters" $namespace) -}}
{{- end -}}
{{- if not (regexMatch "^[a-z][-a-z0-9]*[a-z0-9]$" $namespace) -}}
{{- fail (printf "forge: namespace %q must start with a lowercase letter and contain only lowercase letters, digits and hyphens because it prefixes bootstrap identity names" $namespace) -}}
{{- end -}}
{{- if gt (add (len $release) (len $namespace)) 51 -}}
{{- fail (printf "forge: release name %q (%d characters) and namespace %q (%d characters) total %d; at most 51 keeps %s-%s-management within 63 characters" $release (len $release) $namespace (len $namespace) (add (len $release) (len $namespace)) $namespace $release) -}}
{{- end -}}
{{- end -}}

{{- define "forge.secretMetadata" -}}
namespace: {{ .Release.Namespace }}
annotations:
  helm.sh/resource-policy: keep
labels:
  app.kubernetes.io/managed-by: {{ .Release.Service }}
  app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* A persisted seed is the single source for all derived consumer Secrets. */}}
{{- define "forge.seed" -}}
{{- if not (hasKey . "forgeSeed") -}}
  {{- $name := printf "%s-seed" .Release.Name -}}
  {{- $existing := lookup "v1" "Secret" .Release.Namespace $name -}}
  {{- $keys := list "artifact-admin-password" "artifact-password" "storage-password" "tenant-password" "tenant-encryption-key" "tenant-service-key" "storage-management-token" "s3-user" "s3-password" "sts-user" "sts-password" "oidc-secret" "casdoor-secret" "bootstrap-password" -}}
  {{- $seed := dict -}}
  {{- if $existing -}}
    {{- if ne (index ($existing.metadata.labels | default dict) "app.kubernetes.io/instance" | default "") .Release.Name -}}
      {{- fail "existing Forge seed belongs to another release" -}}
    {{- end -}}
    {{- range $keys -}}
      {{- $value := required (printf "existing Forge seed is missing %s; restore it before upgrading" .) (index $existing.data .) -}}
      {{- $_ := set $seed . ($value | b64dec) -}}
    {{- end -}}
  {{- else -}}
    {{- range $keys -}}{{- $_ := set $seed . (randAlphaNum 48) -}}{{- end -}}
    {{- $_ := set $seed "tenant-encryption-key" (randBytes 32 | b64dec | sha256sum) -}}
    {{- $_ := set $seed "tenant-service-key" (printf "ftk_%s" (randBytes 32 | replace "+" "-" | replace "/" "_" | trimSuffix "=")) -}}
    {{- $_ := set $seed "s3-user" (printf "root%s" (randAlphaNum 16)) -}}
    {{- $_ := set $seed "sts-user" (printf "issuer%s" (randAlphaNum 14)) -}}
    {{- if .Release.IsUpgrade -}}{{- fail "Forge seed is missing; restore it before upgrading" -}}{{- end -}}
    {{- range $suffix := list "artifact-data" "juicefs" -}}
      {{- if lookup "v1" "PersistentVolumeClaim" $.Release.Namespace (printf "%s-%s" $.Release.Name $suffix) -}}
        {{- fail "retained Forge data exists without its credential seed; restore the seed" -}}
      {{- end -}}
    {{- end -}}
    {{- if lookup "v1" "PersistentVolumeClaim" .Release.Namespace (printf "data-%s-tenant-postgres-0" .Release.Name) -}}
      {{- fail "retained Tenant database exists without its credential seed; restore the seed" -}}
    {{- end -}}
  {{- end -}}
  {{- $_ := set . "forgeSeed" $seed -}}
{{- end -}}
{{- end -}}
