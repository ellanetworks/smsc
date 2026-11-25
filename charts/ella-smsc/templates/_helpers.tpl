{{- define "ella-smsc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "ella-smsc.fullname" -}}
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

{{- define "ella-smsc.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "ella-smsc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ella-smsc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "ella-smsc.labels" -}}
helm.sh/chart: {{ include "ella-smsc.chart" . }}
{{ include "ella-smsc.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/component: smsc
app.kubernetes.io/part-of: ella-networks
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "ella-smsc.ha" -}}
{{- if gt (int .Values.replicas) 1 }}true{{ end }}
{{- end }}

{{- define "ella-smsc.headlessServiceName" -}}
{{- printf "%s-headless" (include "ella-smsc.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "ella-smsc.encryptionSecret" -}}
{{- if not ((.Values.config.encryption | default dict).key) }}
{{- default (printf "%s-encryption" (include "ella-smsc.fullname" .)) .Values.encryption.secretName }}
{{- end }}
{{- end }}

{{- define "ella-smsc.clusterSecretName" -}}
{{- default (printf "%s-cluster" (include "ella-smsc.fullname" .)) .Values.cluster.secretName }}
{{- end }}

{{- define "ella-smsc.networkAttachmentName" -}}
{{- default (printf "%s-diameter" (include "ella-smsc.fullname" .)) .Values.diameter.networkAttachment.name }}
{{- end }}

{{- define "ella-smsc.diameterInterface" -}}
{{- if .Values.diameter.networkAttachment.enabled }}
{{- .Values.diameter.networkAttachment.interface }}
{{- else }}
{{- "eth0" }}
{{- end }}
{{- end }}

{{- define "ella-smsc.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "ella-smsc.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "ella-smsc.openshift" -}}
{{- if .Capabilities.APIVersions.Has "security.openshift.io/v1/SecurityContextConstraints" }}true{{ end }}
{{- end }}

{{- define "ella-smsc.image" -}}
{{- $tag := printf "%s%s" (default .Chart.AppVersion .Values.image.tag) .Values.image.tagSuffix }}
{{- if .Values.image.digest }}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}
{{- end }}

{{- define "ella-smsc.config" -}}
{{- $base := dict
  "db" (dict "path" "/data/smsc.db")
  "api" (dict "address" "0.0.0.0" "port" 5010)
  "diameter" (dict "name" (include "ella-smsc.diameterInterface" .) "port" 3868)
}}
{{- if .Values.smpp.enabled }}
{{- $smpp := dict "address" "0.0.0.0" "port" 2775 }}
{{- if .Values.smpp.tls.secretName }}
{{- $_ := set $smpp "tls" (dict "cert" "/tls/tls.crt" "key" "/tls/tls.key") }}
{{- end }}
{{- $_ := set $base "smpp" $smpp }}
{{- end }}
{{- if include "ella-smsc.ha" . }}
{{- $join := printf "%s:7000" (include "ella-smsc.headlessServiceName" .) }}
{{- $_ := set $base "cluster" (dict "address" "0.0.0.0" "port" 7000 "join" (list $join) "bootstrap_expect" 3 "secret_file" "/secrets/cluster/secret") }}
{{- end }}
{{- $encryption := dict }}
{{- if include "ella-smsc.encryptionSecret" . }}
{{- $_ := set $encryption "key" (dict "type" "file" "file" (dict "path" "/secrets/encryption/key")) }}
{{- end }}
{{- if .Values.encryption.previousSecretName }}
{{- $_ := set $encryption "previous_key" (dict "type" "file" "file" (dict "path" "/secrets/encryption-previous/key")) }}
{{- end }}
{{- if $encryption }}
{{- $_ := set $base "encryption" $encryption }}
{{- end }}
{{- if .Values.api.tls.secretName }}
{{- $_ := set $base.api "tls" (dict "cert" "/tls/api/tls.crt" "key" "/tls/api/tls.key") }}
{{- end }}
{{- toYaml (mustMergeOverwrite $base (deepCopy .Values.config)) }}
{{- end }}

{{- define "ella-smsc.validate" -}}
{{- $nad := .Values.diameter.networkAttachment }}
{{- $configured := (.Values.config.encryption | default dict) }}
{{- if and .Values.encryption.secretName $configured.key }}
{{- fail "encryption.secretName and config.encryption.key both set the encryption key: keep one" }}
{{- end }}
{{- if and .Values.encryption.previousSecretName (eq .Values.encryption.previousSecretName (include "ella-smsc.encryptionSecret" .)) }}
{{- fail "encryption.previousSecretName must name another Secret than the encryption key's" }}
{{- end }}
{{- if and .Values.encryption.previousSecretName $configured.previous_key }}
{{- fail "encryption.previousSecretName and config.encryption.previous_key both set the previous encryption key: keep one" }}
{{- end }}

{{- if and $nad.enabled $nad.create (empty $nad.ipam) }}
{{- fail "diameter.networkAttachment.ipam is required when diameter.networkAttachment.create is true" }}
{{- end }}
{{- if and (include "ella-smsc.ha" .) $nad.enabled $nad.create (eq (toString $nad.ipam.type) "static") }}
{{- fail "diameter.networkAttachment.ipam: static IPAM gives every pod the same address; use an IPAM that allocates one address per pod when replicas is more than 1" }}
{{- end }}
{{- end }}

{{- define "ella-smsc.apiScheme" -}}
{{- if .Values.api.tls.secretName }}HTTPS{{ else }}HTTP{{ end }}
{{- end }}
