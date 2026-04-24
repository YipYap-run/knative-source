{{/*
Standard labels applied to every object in this chart.
*/}}
{{- define "yipyap-source.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: yipyap
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
sources.yipyap.run/release: {{ .Chart.AppVersion | quote }}
{{- end -}}

{{/*
Namespace to render into. Honours the release namespace when the user set
namespace.create=false and supplied --namespace themselves.
*/}}
{{- define "yipyap-source.namespace" -}}
{{- if .Values.namespace.create -}}
{{- .Values.namespace.name -}}
{{- else -}}
{{- default .Values.namespace.name .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{/*
Controller image: repo:tag (tag falls back to AppVersion).
*/}}
{{- define "yipyap-source.controllerImage" -}}
{{- $tag := .Values.images.controller.tag | default .Chart.AppVersion -}}
{{ .Values.images.controller.repository }}:{{ $tag }}
{{- end -}}

{{/*
Webhook image: repo:tag.
*/}}
{{- define "yipyap-source.webhookImage" -}}
{{- $tag := .Values.images.webhook.tag | default .Chart.AppVersion -}}
{{ .Values.images.webhook.repository }}:{{ $tag }}
{{- end -}}

{{/*
Receive-adapter image: repo:tag. Consumed by the controller via
YIPYAP_SOURCE_RA_IMAGE; the controller stamps it into the per-source
Deployment it reconciles.
*/}}
{{- define "yipyap-source.adapterImage" -}}
{{- $tag := .Values.images.adapter.tag | default .Chart.AppVersion -}}
{{ .Values.images.adapter.repository }}:{{ $tag }}
{{- end -}}

{{/*
ServiceAccount names.
*/}}
{{- define "yipyap-source.controllerServiceAccountName" -}}
{{- .Values.serviceAccounts.controller.name -}}
{{- end -}}

{{- define "yipyap-source.webhookServiceAccountName" -}}
{{- .Values.serviceAccounts.webhook.name -}}
{{- end -}}
