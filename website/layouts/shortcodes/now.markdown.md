> Act now{{ with .Get "title" }}: {{ . }}{{ end }}
>
{{ .Inner | strings.TrimSpace | replaceRE `(?m)^` "> " }}
