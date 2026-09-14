{{- define "k8s-ops-platform.labels" -}}
app.kubernetes.io/part-of: k8s-ops-platform
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end }}

{{- define "k8s-ops-platform.agentServiceAccount" -}}
{{- .Values.serviceAccount.agentName | default "diagnosis-agent" -}}
{{- end }}

{{- define "k8s-ops-platform.controllerServiceAccount" -}}
{{- .Values.serviceAccount.controllerName | default "action-controller" -}}
{{- end }}
