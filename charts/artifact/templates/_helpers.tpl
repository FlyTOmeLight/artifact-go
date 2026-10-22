{{- define "artifact.forgeEnabled" -}}
{{- if dig "forge" "enabled" false (.Values.global | default dict) -}}true{{- end -}}
{{- end -}}

{{- define "artifact.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if include "artifact.forgeEnabled" . -}}
{{- printf "%s-artifact" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}{{ .Release.Name }}{{- end -}}
{{- end -}}

{{- define "artifact.secret" -}}
{{- if include "artifact.forgeEnabled" . -}}{{ printf "%s-artifact-env" .Release.Name }}
{{- else -}}{{ required "existingSecret is required" .Values.existingSecret }}{{- end -}}
{{- end -}}

{{- define "artifact.tlsSecret" -}}
{{- if include "artifact.forgeEnabled" . -}}{{ printf "%s-tls" .Release.Name }}
{{- else -}}{{ required "tlsSecret is required" .Values.tlsSecret }}{{- end -}}
{{- end -}}

{{- define "artifact.postgresSecret" -}}
{{- if include "artifact.forgeEnabled" . -}}{{ printf "%s-artifact-db" .Release.Name }}
{{- else -}}{{ required "postgres.existingSecret is required" .Values.postgres.existingSecret }}{{- end -}}
{{- end -}}

{{- define "artifact.postgresInitSecret" -}}
{{- if include "artifact.forgeEnabled" . -}}{{ printf "%s-artifact-db-init" .Release.Name }}
{{- else -}}{{ required "postgres.initSecret is required" .Values.postgres.initSecret }}{{- end -}}
{{- end -}}
