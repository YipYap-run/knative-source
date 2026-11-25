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
Image reference for one entry of .Values.images. Takes a dict with name
(the values key), image (the entry) and appVersion. Renders
repository@digest when digest is set, else repository:tag with tag falling
back to appVersion. Setting both tag and digest fails the install, since
the tag would be silently ignored.
*/}}
{{- define "yipyap-source.image" -}}
{{- $img := .image -}}
{{- if $img.digest -}}
{{- if $img.tag -}}
{{- fail (printf "images.%s: set either tag or digest, not both (to deploy a tag, also set images.%s.digest=\"\")" .name .name) -}}
{{- end -}}
{{ $img.repository }}@{{ $img.digest }}
{{- else -}}
{{ $img.repository }}:{{ $img.tag | default .appVersion }}
{{- end -}}
{{- end -}}

{{/*
Controller image.
*/}}
{{- define "yipyap-source.controllerImage" -}}
{{- include "yipyap-source.image" (dict "name" "controller" "image" .Values.images.controller "appVersion" .Chart.AppVersion) -}}
{{- end -}}

{{/*
Webhook image.
*/}}
{{- define "yipyap-source.webhookImage" -}}
{{- include "yipyap-source.image" (dict "name" "webhook" "image" .Values.images.webhook "appVersion" .Chart.AppVersion) -}}
{{- end -}}

{{/*
Receive-adapter image. Consumed by the controller via
YIPYAP_SOURCE_RA_IMAGE; the controller stamps it into the per-source
Deployment it reconciles.
*/}}
{{- define "yipyap-source.adapterImage" -}}
{{- include "yipyap-source.image" (dict "name" "adapter" "image" .Values.images.adapter "appVersion" .Chart.AppVersion) -}}
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

{{/*
NetworkPolicy peers, one ipBlock per CIDR. Takes the CIDR list.
*/}}
{{- define "yipyap-source.ipBlockPeers" -}}
{{- range . }}
- ipBlock:
    cidr: {{ . | quote }}
{{- end }}
{{- end -}}

{{/*
NetworkPolicy TCP ports. Takes the port list.
*/}}
{{- define "yipyap-source.tcpPorts" -}}
{{- range . }}
- protocol: TCP
  port: {{ int . }}
{{- end }}
{{- end -}}

{{/*
CiliumNetworkPolicy TCP ports (Cilium takes ports as strings). Takes the
port list.
*/}}
{{- define "yipyap-source.ciliumPorts" -}}
{{- range . }}
- port: {{ int . | toString | quote }}
  protocol: TCP
{{- end }}
{{- end -}}

{{/*
Comma separated base URL host allowlist for YIPYAP_ALLOWED_BASE_URL_HOSTS
on the webhook and controller. Fails the install when the list is empty,
since an empty allowlist would reject every YipYapSource.
*/}}
{{- define "yipyap-source.allowedBaseURLHosts" -}}
{{- $hosts := .Values.webhook.allowedBaseURLHosts | default list -}}
{{- if not $hosts -}}
{{- fail "webhook.allowedBaseURLHosts must list at least one host (default: console.yipyap.run)" -}}
{{- end -}}
{{- join "," $hosts -}}
{{- end -}}
