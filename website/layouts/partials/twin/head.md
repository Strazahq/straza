{{- /* The head of a page's Markdown copy for agents: the title, the outcome
line, the task fields of a tutorial or how-to, and In short. */ -}}
# {{ .Params.heading | default .Title }}
{{ with .Description }}
{{ . }}
{{ end }}
{{- if in (slice "tutorial" "how-to") .Params.pagetype }}
{{- $profiles := dict "standalone" "Standalone" "enterprise" "Enterprise" "both" "Standalone and enterprise" -}}
{{- $p := .Params }}
{{ with $p.who }}- Who: {{ . }}
{{ end }}{{ with $p.where }}- Where: {{ . }}
{{ end }}{{ with $p.applies_to }}- Profile: {{ index $profiles . | default . }}
{{ end }}{{ with $p.time }}- Time: {{ . }}
{{ end }}
{{- end }}
{{- with .Params.summary }}
In short: {{ . }}
{{ end }}
