{{ index (dict "cli" "CLI only" "console" "Console only") (.Get "form") }}: {{ .Inner | strings.TrimSpace }}
