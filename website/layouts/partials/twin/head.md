{{- /* The head of a page's Markdown copy for agents: the title, the outcome
line, the task fields of a tutorial or how-to, and In short. */ -}}
# {{ .Params.heading | default .Title }}
{{ with .Description }}
{{ . }}
{{ end }}
{{- if in (slice "tutorial" "how-to") .Params.pagetype }}
{{- /* The profile line shows only when the steps hold for one profile, as on
the page itself. */ -}}
{{- $profiles := dict "standalone" "Standalone only" "enterprise" "Enterprise only" -}}
{{- $p := .Params }}
{{ with $p.who }}- Who: {{ . }}
{{ end }}{{ with $p.where }}- Where: {{ . }}
{{ end }}{{ with index $profiles ($p.applies_to | default "") }}- Profile: {{ . }}
{{ end }}
{{- end }}
{{- with .Params.summary }}
In short: {{ . }}
{{ end }}
