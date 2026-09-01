package mesh

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// splitFrontmatter separates a leading `---\n...\n---\n` YAML block from the
// markdown body. If there is no frontmatter, fm is empty and body is the whole
// input.
func splitFrontmatter(s string) (fm, body string) {
	s = strings.TrimPrefix(s, "\ufeff") // strip BOM if present
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", s
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	// Find the closing delimiter line.
	lines := strings.Split(rest, "\n")
	for i, ln := range lines {
		if strings.TrimRight(ln, "\r") == "---" {
			fm = strings.Join(lines[:i], "\n")
			body = strings.Join(lines[i+1:], "\n")
			return fm, strings.TrimLeft(body, "\n")
		}
	}
	return "", s
}

func decodeYAML(s string, v any) error {
	dec := yaml.NewDecoder(strings.NewReader(s))
	dec.KnownFields(false)
	return dec.Decode(v)
}
