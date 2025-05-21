{{ range . }}
### `ariga/atlas-action/{{ .ID }}`

{{ .Description }}
{{ if .Inputs }}
#### Inputs

All inputs are optional as they may be specified in the Atlas configuration file.
{{ range $name, $item := .SortedInputs }}
* `{{ $name }}` - {{ if not $item.Required }}(Optional) {{end}}{{ $item.Description | trimnl | nl2sp }}
{{- end }}
{{ end -}}
{{ if .Outputs }}
#### Outputs
{{ range $name, $item := .SortedOutputs }}
* `{{ $name }}` - {{ $item.Description | trimnl | nl2sp }}
{{- end }}
{{ end }}
{{ end }}
