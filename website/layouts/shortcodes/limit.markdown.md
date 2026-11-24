Who can reach it: {{ .Get "who" }}. Roadmap: {{ .Get "status" }}.

{{ .Inner | strings.TrimSpace }}
