{{- $terminal := .Get "terminal" -}}
{{- $purpose := .Get "purpose" -}}
{{- if and (not $purpose) (strings.Contains $terminal " · ") -}}
  {{- $parts := split $terminal " · " -}}
  {{- $terminal = index $parts 0 -}}
  {{- $purpose = delimit (after 1 $parts) " · " -}}
{{- end -}}
{{ with $terminal }}{{ . }}{{ with $purpose }}, {{ . }}{{ end }}:{{ end }}
{{ .Inner }}
