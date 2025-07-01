{{- define "migrate-lint/md" -}}
`atlas migrate lint` on **{{ if eq .Env.Dir "." }}working directory{{ else }}{{ .Env.Dir }}{{ end }}**

| Status | Step | Result |
| :----: | :--- | :----- |
| {{ template "lint-check/md" "success.svg" }} | {{ filesDetected .Files }} | {{ join (fileNames .Files) " " }} |
{{ template "lint-report/md" . }}
{{- end -}}
{{- define "schema-plan/md" -}}
### Atlas detected changes to the desired schema

{{ with .File -}}
#### Migration Plan | Repository: {{ $.Repo }}{{- with .Link }} ([View on Atlas Cloud]({{- . -}})){{ end }}
{{- with .Migration -}}{{- codeblock "sql" . -}}{{- end }}
{{- end -}}
#### Atlas lint results

| Status | Step | Result |
| :----: | :--- | :----- |
| {{ template "lint-check/md" "success.svg" }} | {{ with .File }}Detect schema changes | {{ stmtsDetected . }}{{ else -}} | {{ end }} |
{{ template "lint-report/md" .Lint -}}
{{- with .File }}

---

##### 📝 Steps to edit this migration plan

{{ template "plan-modify/md" . }}
{{- end -}}
{{- end -}}
{{- define "lint-report/md" -}}
{{- with .URL -}}
| {{ template "lint-check/md" "success.svg" }} | ERD and visual diff generated | [View Visualization]({{- printf "%s#erd" . -}}) |
{{ end }}
{{- with (.Steps | filterIssues) -}}
{{ range $step := . -}}
| {{ template "lint-check/md" (or (and ($step | stepIsError) "error.svg") "warning.svg") }} | {{ stepSummary $step | nl2sp }} | {{ stepDetails $step | nl2sp }} |
{{ end -}}
{{- else -}}
| {{ template "lint-check/md" "success.svg" }} | No issues found | {{ with .URL -}}[View Report]({{- . -}}){{- end }} |
{{- end -}}
{{- end -}}