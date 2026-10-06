{{/* Expand the name of the chart. */}}
{{- define "jiramcp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "jiramcp.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* True when the server holds service-account credentials (not jira mode). */}}
{{- define "jiramcp.usesServiceAccount" -}}
{{- if not (and (eq .Values.mcp.transport "http") (eq .Values.mcp.authMode "jira")) -}}true{{- end -}}
{{- end -}}

{{/* True when a team mapping is configured. */}}
{{- define "jiramcp.hasMapping" -}}
{{- if trim .Values.teamMapping.inlineYaml -}}true{{- end -}}
{{- end -}}

{{- define "jiramcp.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "jiramcp.labels" -}}
helm.sh/chart: {{ include "jiramcp.chart" . }}
{{ include "jiramcp.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "jiramcp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "jiramcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "jiramcp.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "jiramcp.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Full image reference: [registry/]repository:tag (registry optional). */}}
{{- define "jiramcp.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- if .Values.image.registry -}}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repository $tag -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding credentials (existing or chart-managed). */}}
{{- define "jiramcp.secretName" -}}
{{- if .Values.jira.existingSecret -}}
{{- .Values.jira.existingSecret -}}
{{- else -}}
{{- include "jiramcp.fullname" . -}}
{{- end -}}
{{- end -}}

{{/* Validate required configuration early with a clear message. */}}
{{- define "jiramcp.validate" -}}
{{- if not .Values.jira.baseUrl -}}
{{- fail "jira.baseUrl is required" -}}
{{- end -}}
{{- if not (has .Values.mcp.authMode (list "token" "jira")) -}}
{{- fail "mcp.authMode must be token or jira" -}}
{{- end -}}
{{- if and (eq .Values.mcp.transport "http") (eq .Values.mcp.authMode "token") -}}
{{- if and (not .Values.mcp.allowUnauthenticated) (not .Values.jira.existingSecret) (not .Values.mcp.authToken) -}}
{{- fail "mcp.authToken (or jira.existingSecret with an MCP_AUTH_TOKEN key) is required for http transport; set mcp.allowUnauthenticated=true only for local dev" -}}
{{- end -}}
{{- end -}}
{{- end -}}
