package markdown

// Front matter detection follows gopherwiki's internal/frontmatter package;
// see LICENSE.gopherwiki.

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// Front is a page's optional YAML front matter.
type Front struct {
	Title     string  `yaml:"title"`
	Type      string  `yaml:"type"`
	Tags      Strings `yaml:"tags"`
	Status    string  `yaml:"status"`
	Priority  string  `yaml:"priority"`
	Due       string  `yaml:"due"`
	Assignees Strings `yaml:"assignees"`

	// Raw is the whole mapping, including keys gnotes does not use.
	Raw map[string]any `yaml:"-"`
}

// Strings is a YAML list of strings that also accepts a single scalar.
type Strings []string

// UnmarshalYAML reads a sequence or a scalar.
func (s *Strings) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value != "" {
			*s = Strings{n.Value}
		}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return err
	}
	*s = list
	return nil
}

// splitFront returns the front matter and the offset where the body starts.
//
// Detection is conservative: the block must open with a "---" line at the very
// start, close with "---" or "...", and decode as a mapping. Anything else is
// body text, such as a thematic break, and yields nil and 0.
func splitFront(src []byte) (*Front, int) {
	start := 0
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		start = 3
	}
	line, next := nextLine(src, start)
	if string(trimCR(line)) != "---" {
		return nil, 0
	}

	blockStart := next
	for pos := next; pos < len(src); {
		line, next = nextLine(src, pos)
		if l := string(trimCR(line)); l == "---" || l == "..." {
			block := src[blockStart:pos]
			var raw map[string]any
			if yaml.Unmarshal(block, &raw) != nil || raw == nil && len(bytes.TrimSpace(block)) > 0 {
				return nil, 0
			}
			f := &Front{Raw: raw}
			if yaml.Unmarshal(block, f) != nil {
				return nil, 0
			}
			return f, next
		}
		pos = next
	}
	return nil, 0
}

// nextLine returns the line starting at pos, without its newline, and the
// offset of the following line.
func nextLine(src []byte, pos int) ([]byte, int) {
	if i := bytes.IndexByte(src[pos:], '\n'); i >= 0 {
		return src[pos : pos+i], pos + i + 1
	}
	return src[pos:], len(src)
}

func trimCR(b []byte) []byte { return bytes.TrimSuffix(b, []byte("\r")) }
