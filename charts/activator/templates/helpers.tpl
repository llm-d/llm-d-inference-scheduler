{{- define "activatorName" -}}
{{ .Values.route.name }}{{ .Values.activator.suffix }}
{{- end -}}
