// Package templates embeds the rule and config templates rule-cli writes when
// scaffolding a new project, so the binary carries them and needs no files
// installed alongside it.
package templates

import "embed"

// TemplateFS holds the embedded YAML template files for rule creation.
//
//go:embed *.yaml
var TemplateFS embed.FS
