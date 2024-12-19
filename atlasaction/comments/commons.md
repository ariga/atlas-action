{{- define "plan-modify/md" -}}
1\. Run the following command to pull the generated plan to your local workstation:
```bash
atlas schema plan pull --url "{{ .URL }}" > {{ .Name }}.plan.hcl
```

2\. Open `{{ .Name }}` in your editor and modify it as needed. Note that the result of the plan should align
the database with the desired state. Otherwise, Atlas will report a schema drift.

3\. Push the updated plan to the registry using the following command:
```bash
atlas schema plan push --pending --file {{ .Name }}.plan.hcl
```
{{- end -}}
{{- define "lint-check" -}}
{{- assetsImage . | image "20px" -}}
{{- end -}}
