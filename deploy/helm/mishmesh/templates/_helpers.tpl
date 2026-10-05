{{- define "mishmesh.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mishmesh.fullname" -}}
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

{{- define "mishmesh.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mishmesh.baseSelectorLabels" -}}
app.kubernetes.io/name: {{ include "mishmesh.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "mishmesh.selectorLabels" -}}
{{ include "mishmesh.baseSelectorLabels" . }}
app.kubernetes.io/component: server
{{- end -}}

{{- define "mishmesh.commonLabels" -}}
helm.sh/chart: {{ include "mishmesh.chart" . }}
{{ include "mishmesh.baseSelectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: mishmesh
{{- end -}}

{{- define "mishmesh.labels" -}}
{{ include "mishmesh.commonLabels" . }}
app.kubernetes.io/component: server
{{- end -}}

{{- define "mishmesh.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "mishmesh.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "mishmesh.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{- define "mishmesh.secretName" -}}
{{- default (include "mishmesh.fullname" .) .Values.secrets.existingSecret -}}
{{- end -}}

{{- define "mishmesh.builtinPostgres" -}}
{{- $pg := .Values.postgres -}}
{{- if and $pg.builtin.enabled (not $pg.external.url) (not $pg.external.existingSecret) -}}
true
{{- end -}}
{{- end -}}

{{- define "mishmesh.postgresName" -}}
{{- printf "%s-postgres" (include "mishmesh.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "mishmesh.dsnSecretRef" -}}
{{- if include "mishmesh.builtinPostgres" . -}}
{{- dict "name" (include "mishmesh.postgresName" .) "key" "data-dsn" | toJson -}}
{{- else -}}
{{- dict "name" (default (include "mishmesh.secretName" .) .Values.postgres.external.existingSecret) "key" .Values.postgres.external.existingSecretKey | toJson -}}
{{- end -}}
{{- end -}}

{{- define "mishmesh.redisSecretName" -}}
{{- default (include "mishmesh.secretName" .) .Values.connStore.redis.existingSecret -}}
{{- end -}}

{{- define "mishmesh.sshKeySecretName" -}}
{{- default (include "mishmesh.secretName" .) .Values.ssh.hostKey.existingSecret -}}
{{- end -}}

{{- define "mishmesh.sshKeyMounted" -}}
{{- if and .Values.listeners.ssh.enabled (or .Values.ssh.hostKey.existingSecret .Values.ssh.hostKey.generate) -}}
true
{{- end -}}
{{- end -}}

{{- define "mishmesh.multiReplica" -}}
{{- if or (gt (int .Values.replicaCount) 1) .Values.autoscaling.enabled -}}
true
{{- end -}}
{{- end -}}

{{- define "mishmesh.stableValue" -}}
{{- $existing := lookup "v1" "Secret" .root.Release.Namespace (default (include "mishmesh.fullname" .root) .secret) -}}
{{- if .value -}}
{{- .value -}}
{{- else if and $existing $existing.data (hasKey $existing.data .key) -}}
{{- index $existing.data .key | b64dec -}}
{{- else if eq (default "" .kind) "ed25519" -}}
{{- genPrivateKey "ed25519" -}}
{{- else -}}
{{- randAlphaNum 48 -}}
{{- end -}}
{{- end -}}

{{- define "mishmesh.tcpPorts" -}}
{{- $ports := list -}}
{{- if .Values.listeners.tcp.enabled -}}
{{- range $p := untilStep (int .Values.listeners.tcp.portMin) (int (add1 .Values.listeners.tcp.portMax)) 1 -}}
{{- $ports = append $ports $p -}}
{{- end -}}
{{- end -}}
{{- toJson $ports -}}
{{- end -}}

{{- define "mishmesh.validate" -}}
{{- $v := .Values -}}
{{- if not (has $v.connStore.backend (list "memory" "redis")) -}}
{{- fail "connStore.backend must be memory or redis" -}}
{{- end -}}
{{- if and (include "mishmesh.multiReplica" .) (not $v.cluster.enabled) -}}
{{- fail "replicaCount > 1 or autoscaling.enabled requires cluster.enabled=true" -}}
{{- end -}}
{{- if and $v.cluster.enabled (ne $v.connStore.backend "redis") -}}
{{- fail "cluster.enabled requires connStore.backend=redis" -}}
{{- end -}}
{{- if not (or (include "mishmesh.builtinPostgres" .) $v.postgres.external.url $v.postgres.external.existingSecret $v.secrets.existingSecret) -}}
{{- fail "postgres is required: enable postgres.builtin, or set postgres.external.url, postgres.external.existingSecret, or secrets.existingSecret holding data-dsn" -}}
{{- end -}}
{{- if and (eq $v.connStore.backend "redis") (not $v.connStore.redis.url) (not $v.connStore.redis.existingSecret) (not $v.secrets.existingSecret) -}}
{{- fail "connStore.backend=redis needs connStore.redis.url, connStore.redis.existingSecret, or secrets.existingSecret holding redis-url" -}}
{{- end -}}
{{- if and $v.listeners.tcp.enabled (lt (int $v.listeners.tcp.portMax) (int $v.listeners.tcp.portMin)) -}}
{{- fail "listeners.tcp.portMax must be >= listeners.tcp.portMin" -}}
{{- end -}}
{{- if and $v.listeners.https.enabled (not (or $v.tls.existingSecret $v.tls.acme.enabled $v.tls.selfSigned)) -}}
{{- fail "listeners.https.enabled needs tls.existingSecret, tls.acme.enabled, or tls.selfSigned" -}}
{{- end -}}
{{- if and $v.connectIngress.enabled (not $v.connectIngress.host) -}}
{{- fail "connectIngress.enabled requires connectIngress.host (e.g. connect.<baseDomain>)" -}}
{{- end -}}
{{- end -}}
