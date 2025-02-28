{{ if gt (len .Arguments) 0 -}}
    (
    {{- range $i,$v:= .Arguments -}}
        {{ if gt $i 0 -}},{{ end -}}
        {{- $v.Name -}}:{{ $v.Type }}
        {{- if $v.Default -}}
            = {{- $v.Default -}}
        {{- end -}}
    {{- end -}}
    )
{{- end -}}