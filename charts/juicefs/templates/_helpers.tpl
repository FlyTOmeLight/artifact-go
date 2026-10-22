{{- define "juicefs.fullname" -}}
{{- default (printf "%s-juicefs" .Release.Name) .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "juicefs.credentialsSecret" -}}
{{- if .Values.credentials.existingSecret -}}
{{- .Values.credentials.existingSecret -}}
{{- else if .Values.global.forge.enabled -}}
{{- printf "%s-storage-credentials" .Release.Name -}}
{{- else -}}
{{- printf "%s-credentials" (include "juicefs.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "juicefs.claimName" -}}
{{- default (include "juicefs.fullname" .) .Values.persistence.existingClaim -}}
{{- end -}}

{{- define "juicefs.selectorLabels" -}}
app.kubernetes.io/name: juicefs
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "juicefs.labels" -}}
{{ include "juicefs.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}
