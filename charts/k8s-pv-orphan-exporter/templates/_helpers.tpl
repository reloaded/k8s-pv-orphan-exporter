{{/*
Chart name (optionally overridden).
*/}}
{{- define "kpoe.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified app name. Honors fullnameOverride; otherwise
release-name-prefixed unless the release name already contains the
chart name (the common Helm convention).
*/}}
{{- define "kpoe.fullname" -}}
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

{{- define "kpoe.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels — applied to every object. Stable across upgrades
except `version`/`helm.sh/chart`, which is why they are NOT used in
selectors (see kpoe.selectorLabels).
*/}}
{{- define "kpoe.labels" -}}
helm.sh/chart: {{ include "kpoe.chart" . }}
app.kubernetes.io/name: {{ include "kpoe.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: pv-orphan-exporter
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
Selector labels for a given component. Call:
  include "kpoe.selectorLabels" (dict "ctx" . "component" "local-path-scanner")
Immutable set only — never add chart/version here or upgrades break
the DaemonSet/Deployment selector.
*/}}
{{- define "kpoe.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kpoe.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Resolved container image reference.
*/}}
{{- define "kpoe.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
ServiceAccount name.
*/}}
{{- define "kpoe.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kpoe.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
