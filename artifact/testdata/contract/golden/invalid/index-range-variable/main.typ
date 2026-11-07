[{{ range $e := .data.items }}{{ if $.data.flag }}{{ index $e "x" }}{{ end }}{{ end }}]
