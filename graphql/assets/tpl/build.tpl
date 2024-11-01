"""
A cursor is an encoded string use for pagination
"""
scalar Cursor
"""
The `DateTime` scalar type represents a DateTime. The DateTime is serialized as an RFC 3339 quoted string
"""
scalar DateTime

"The direction of result ordering."
enum SortInput {
    "Ascending order"
    ASC
    "Descending order"
    DESC
    "Ascending nulls first order"
    ASC_NULLS_FIRST
    "Descending nulls first order"
    DESC_NULLS_FIRST
    "Ascending nulls last order"
    ASC_NULLS_LAST
    "Descending nulls last order"
    DESC_NULLS_LAST
}

"NULL or NOT"
enum IsInput {
    NULL
    NOT_NULL
}

{{ range $key,$obj := . }}
    {{- if $obj.Description }}
        """
        {{ $obj.Description }}
        """
    {{ end }}

    {{- if eq $obj.Kind "INPUT_OBJECT" -}}
        input {{ $key }} {
    {{- else -}}
        type {{ $key }} {
    {{- end -}}

    {{- range $obj.Fields }}
        {{- if .Description }}
            """
            {{ .Description }}
            """
        {{- end }}
        {{ .Name }}{{template "input.tpl" .}}: {{ .Type }}
    {{- end }}
    }
{{ end }}