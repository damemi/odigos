{{- define "utils.certManagerApiVersion" -}}
{{- if .Values.certManagerApiVersion -}} 
  {{- .Values.certManagerApiVersion -}}
{{- else -}}
{{- print "" -}}
{{- end -}}
{{- end -}}