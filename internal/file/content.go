package file

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"
)

func resolveContent(param, targetPath string) ([]byte, error) {
	var content []byte
	var err error

	if strings.HasPrefix(param, "source://") {
		sourcePath := strings.TrimPrefix(param, "source://")
		content, err = os.ReadFile(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read source file %s: %w", sourcePath, err)
		}
	} else {
		content = []byte(param)
	}

	if strings.HasSuffix(targetPath, ".tmpl") || (strings.HasPrefix(param, "source://") && strings.HasSuffix(strings.TrimPrefix(param, "source://"), ".tmpl")) {
		return applyTemplate(content, param)
	}

	return content, nil
}

func applyTemplate(content []byte, param string) ([]byte, error) {
	data := make(map[string]string)
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if len(pair) == 2 {
			data[pair[0]] = pair[1]
		}
	}
	data["Param"] = param

	tmpl, err := template.New("resource").Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("template parse error: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template execute error: %w", err)
	}
	return buf.Bytes(), nil
}
