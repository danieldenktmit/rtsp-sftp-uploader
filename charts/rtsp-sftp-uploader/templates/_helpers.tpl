{{/*
Expand the name of the chart.
*/}}
{{- define "rtsp-sftp-uploader.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name, capped at 63 characters for DNS compatibility.
*/}}
{{- define "rtsp-sftp-uploader.fullname" -}}
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

{{- define "rtsp-sftp-uploader.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "rtsp-sftp-uploader.labels" -}}
helm.sh/chart: {{ include "rtsp-sftp-uploader.chart" . }}
{{ include "rtsp-sftp-uploader.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "rtsp-sftp-uploader.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rtsp-sftp-uploader.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "rtsp-sftp-uploader.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "rtsp-sftp-uploader.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding credentials: either an existing one or the chart's own.
*/}}
{{- define "rtsp-sftp-uploader.secretName" -}}
{{- default (include "rtsp-sftp-uploader.fullname" .) .Values.existingSecret }}
{{- end }}

{{- define "rtsp-sftp-uploader.remoteFilename" -}}
{{- default .Values.capture.filename .Values.sftp.remoteFilename }}
{{- end }}

{{/*
Whether the chart manages its own Secret.
*/}}
{{- define "rtsp-sftp-uploader.createSecret" -}}
{{- if .Values.existingSecret }}{{- "" }}{{- else }}{{- "true" }}{{- end }}
{{- end }}

{{/*
Whether any file needs to be mounted from the Secret.
*/}}
{{- define "rtsp-sftp-uploader.hasSecretFiles" -}}
{{- if or .Values.sftp.privateKey .Values.sftp.knownHosts }}{{- "true" }}{{- end }}
{{- end }}

{{- define "rtsp-sftp-uploader.secretMountPath" -}}
/etc/rtsp-sftp-uploader
{{- end }}

{{/*
Fail fast on configurations that would only surface as a CrashLoopBackOff.
*/}}
{{- define "rtsp-sftp-uploader.validate" -}}
{{- if and (not .Values.rtsp.url) (not .Values.rtsp.host) }}
{{- fail "rtsp-sftp-uploader: set either rtsp.url or rtsp.host" }}
{{- end }}
{{- if and (not .Values.sftp.url) (not .Values.sftp.host) }}
{{- fail "rtsp-sftp-uploader: set either sftp.url or sftp.host" }}
{{- end }}
{{- if and (not .Values.sftp.username) (not .Values.sftp.url) }}
{{- fail "rtsp-sftp-uploader: set sftp.username" }}
{{- end }}
{{- if and (not .Values.sftp.password) (not .Values.sftp.privateKey) (not .Values.sftp.url) (not .Values.existingSecret) }}
{{- fail "rtsp-sftp-uploader: provide a credential: set sftp.password, sftp.privateKey, or existingSecret" }}
{{- end }}
{{- $hostKeyStrategies := 0 }}
{{- if .Values.sftp.knownHosts }}{{- $hostKeyStrategies = add1 $hostKeyStrategies }}{{- end }}
{{- if .Values.sftp.hostKeyFingerprint }}{{- $hostKeyStrategies = add1 $hostKeyStrategies }}{{- end }}
{{- if .Values.sftp.insecureIgnoreHostKey }}{{- $hostKeyStrategies = add1 $hostKeyStrategies }}{{- end }}
{{- if eq $hostKeyStrategies 0 }}
{{- fail "rtsp-sftp-uploader: configure host key verification: set sftp.knownHosts or sftp.hostKeyFingerprint, or explicitly set sftp.insecureIgnoreHostKey=true" }}
{{- end }}
{{- if gt $hostKeyStrategies 1 }}
{{- fail "rtsp-sftp-uploader: sftp.knownHosts, sftp.hostKeyFingerprint and sftp.insecureIgnoreHostKey are mutually exclusive; choose one" }}
{{- end }}
{{- if gt (int .Values.replicaCount) 1 }}
{{- fail "rtsp-sftp-uploader: replicaCount must be 1; multiple replicas would overwrite the same remote file" }}
{{- end }}
{{- end }}
