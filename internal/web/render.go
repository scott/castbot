package web

import (
	"bytes"
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
	"modelLabel": modelLabel,
}).ParseFS(templateFS, "templates/*.html"))

func execTemplate(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := views.ExecuteTemplate(&buf, name, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
