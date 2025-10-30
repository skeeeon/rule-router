// file: cmd/rule-cli/templates/embed.go
package templates

import "embed"

// TemplateFS holds the embedded YAML template files for rule creation.
//
//go:embed *.yaml
var TemplateFS embed.FS
