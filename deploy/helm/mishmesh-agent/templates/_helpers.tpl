{{- define "mishmesh-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mishmesh-agent.fullname" -}}
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

{{- define "mishmesh-agent.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "mishmesh-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: mishmesh
{{- end -}}

{{- define "mishmesh-agent.tokenRef" -}}
{{- $root := .root -}}
{{- $t := .tunnel -}}
{{- if $t.existingSecret -}}
{{- dict "name" $t.existingSecret "key" (default "token" $t.existingSecretKey) | toJson -}}
{{- else if $t.token -}}
{{- dict "name" (include "mishmesh-agent.fullname" $root) "key" (printf "token-%s" $t.name) | toJson -}}
{{- else if $root.Values.existingSecret -}}
{{- dict "name" $root.Values.existingSecret "key" $root.Values.existingSecretKey | toJson -}}
{{- else if $root.Values.token -}}
{{- dict "name" (include "mishmesh-agent.fullname" $root) "key" "token" | toJson -}}
{{- else -}}
{{- fail (printf "tunnel %q has no token: set token, existingSecret, or tunnels[].token / tunnels[].existingSecret" $t.name) -}}
{{- end -}}
{{- end -}}

{{- define "mishmesh-agent.args" -}}
{{- $t := . -}}
{{- $args := list $t.kind $t.target -}}
{{- if and (eq $t.kind "http") $t.subdomain -}}
{{- $args = concat $args (list "--subdomain" $t.subdomain) -}}
{{- end -}}
{{- if and (eq $t.kind "tcp") $t.port -}}
{{- $args = concat $args (list "--port" (toString (int $t.port))) -}}
{{- end -}}
{{- if $t.reserved -}}
{{- $args = append $args "--reserved" -}}
{{- end -}}
{{- if $t.targetHTTPS -}}
{{- $args = append $args "--target-https" -}}
{{- end -}}
{{- if $t.insecureSkipVerify -}}
{{- $args = append $args "--insecure" -}}
{{- end -}}
{{- $args = concat $args (default (list) $t.extraArgs) -}}
{{- toJson $args -}}
{{- end -}}
