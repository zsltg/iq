{{ if .Versions -}}
# CHANGELOG

All notable changes to this project are documented here.
This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
and its commits follow [Conventional Commits](https://www.conventionalcommits.org/).
{{ end -}}

{{ range .Versions }}
## {{ .Tag.Name }} - {{ datetime "2006-01-02" .Tag.Date }}
{{ range .CommitGroups -}}
### {{ .Title }}
{{ range .Commits -}}
- {{ if .Scope }}**{{ .Scope }}:** {{ end }}{{ .Subject }} ({{ .Hash.Short }})
{{ end }}
{{ end -}}
{{ end -}}
