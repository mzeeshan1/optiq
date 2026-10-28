{{/*
Expand the name of the chart.
*/}}
{{- define "optiq.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a fully qualified app name.
*/}}
{{- define "optiq.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "optiq.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{ include "optiq.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "optiq.selectorLabels" -}}
app.kubernetes.io/name: {{ include "optiq.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Ingress annotations
*/}}
{{- define "optiq.ingress.annotations" -}}
{{- with .Values.ingress.annotations }}
{{- toYaml . }}
{{- end }}
{{- end }}
{{/*
Postgres service name — used to build DATABASE_URL
*/}}
{{- define "optiq.postgresHost" -}}
{{- printf "%s-postgres" (include "optiq.fullname" .) }}
{{- end }}

{{/*
Redis service name — used to build REDIS_URL
*/}}
{{- define "optiq.redisHost" -}}
{{- printf "%s-redis" (include "optiq.fullname" .) }}
{{- end }}
