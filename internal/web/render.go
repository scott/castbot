package web

import (
	"embed"
	"html/template"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

var views = template.Must(template.New("root").Funcs(template.FuncMap{
	"firstName": func(name string) string {
		parts := strings.Fields(name)
		if len(parts) == 0 {
			return name
		}
		return parts[0]
	},
}).ParseFS(templateFS, "templates/*.html"))
