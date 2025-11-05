// file: internal/cli/renderer.go
package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"rule-router/cmd/rule-cli/templates"
)

// Renderer handles the logic for listing, showing, and rendering rule templates.
type Renderer struct {
	templateFS fs.FS
}

// NewRenderer creates a new template renderer.
func NewRenderer() *Renderer {
	return &Renderer{
		templateFS: templates.TemplateFS,
	}
}

// ListTemplates returns a list of available template names.
func (r *Renderer) ListTemplates() ([]string, error) {
	var templateNames []string
	err := fs.WalkDir(r.templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// The path from WalkDir is already relative to the embed root.
		// No need to trim any "templates/" prefix.
		if !d.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			name := strings.TrimSuffix(path, filepath.Ext(path))
			templateNames = append(templateNames, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk embedded templates: %w", err)
	}
	return templateNames, nil
}

// GetTemplateContent returns the raw content of a specific template.
func (r *Renderer) GetTemplateContent(templateName string) (string, error) {
	// The path inside the embed.FS does not include the "templates/" directory.
	// The embed root *is* the templates directory.
	filePath := fmt.Sprintf("%s.yaml", templateName)
	content, err := fs.ReadFile(r.templateFS, filePath)
	if err != nil {
		// Try .yml as a fallback
		filePath = fmt.Sprintf("%s.yml", templateName)
		content, err = fs.ReadFile(r.templateFS, filePath)
		if err != nil {
			return "", fmt.Errorf("template '%s' not found", templateName)
		}
	}
	return string(content), nil
}

// RenderTemplate generates a rule file from a template.
func (r *Renderer) RenderTemplate(templateName, outputPath string) error {
	content, err := r.GetTemplateContent(templateName)
	if err != nil {
		return err
	}

	// Check if the file already exists before writing.
	if _, err := os.Stat(outputPath); err == nil {
		fmt.Printf("File '%s' already exists. Overwrite? (y/N): ", outputPath)
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	err = os.WriteFile(outputPath, []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("failed to write rule file '%s': %w", outputPath, err)
	}

	fmt.Printf("✓ Success! Rule file '%s' created from template '%s'.\n", outputPath, templateName)
	return nil
}

// RenderTemplateWithData renders a template with provided data.
func (r *Renderer) RenderTemplateWithData(templateName string, data map[string]string) (string, error) {
	content, err := r.GetTemplateContent(templateName)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New(templateName).Parse(content)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}
