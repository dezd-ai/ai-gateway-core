package config

import (
	"fmt"
	"strconv"
	"strings"
)

// yaml.go 实现一个仅依赖标准库的最小 YAML 子集解析器。
// 覆盖本网关 config.yaml 所需的全部语法：缩进嵌套映射、列表、行内列表、
// 引号字符串、整数/浮点/布尔、null、注释。

type yline struct {
	indent int
	text   string
}

type yparser struct {
	lines []yline
	pos   int
}

// ParseYAML 将 YAML 字节流解析为通用 any 树（map[string]any / []any / 标量）。
func ParseYAML(data []byte) (map[string]any, error) {
	lines := preprocessYAML(data)
	if len(lines) == 0 {
		return map[string]any{}, nil
	}
	p := &yparser{lines: lines}
	v, err := p.parseBlockAny(lines[0].indent)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("yaml: 根节点必须是映射")
	}
	return m, nil
}

func preprocessYAML(data []byte) []yline {
	var out []yline
	for _, raw := range strings.Split(string(data), "\n") {
		raw = strings.TrimRight(raw, "\r")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		indent := 0
		for indent < len(raw) && raw[indent] == ' ' {
			indent++
		}
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = stripYAMLComment(text)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, yline{indent: indent, text: text})
	}
	return out
}

func stripYAMLComment(s string) string {
	var inSingle, inDouble bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '#' && !inSingle && !inDouble:
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}

func (p *yparser) parseBlockAny(indent int) (any, error) {
	if p.pos >= len(p.lines) || p.lines[p.pos].indent < indent {
		return map[string]any{}, nil
	}
	if p.lines[p.pos].indent > indent {
		return nil, fmt.Errorf("yaml: 第 %d 行缩进异常", p.pos+1)
	}
	if isYAMLListItem(p.lines[p.pos].text) {
		return p.parseList(indent)
	}
	return p.parseMap(indent)
}

func (p *yparser) parseMap(indent int) (map[string]any, error) {
	m := map[string]any{}
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent > indent {
			return nil, fmt.Errorf("yaml: 第 %d 行意外缩进 %q", p.pos+1, l.text)
		}
		idx := findYAMLColon(l.text)
		if idx < 0 {
			return nil, fmt.Errorf("yaml: 第 %d 行缺少 key: value 分隔 %q", p.pos+1, l.text)
		}
		key := strings.TrimSpace(l.text[:idx])
		rest := strings.TrimSpace(l.text[idx+1:])
		p.pos++
		if rest == "" {
			childIndent := p.nextIndent(indent)
			child, err := p.parseBlockAny(childIndent)
			if err != nil {
				return nil, err
			}
			if child == nil {
				child = map[string]any{}
			}
			m[key] = child
		} else {
			m[key] = parseYAMLScalar(rest)
		}
	}
	return m, nil
}

func (p *yparser) parseList(indent int) ([]any, error) {
	var list []any
	for p.pos < len(p.lines) {
		l := p.lines[p.pos]
		if l.indent < indent {
			break
		}
		if l.indent != indent || !isYAMLListItem(l.text) {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l.text), "-"))
		item = strings.TrimSpace(item)
		p.pos++

		if item == "" {
			childIndent := p.nextIndent(indent)
			child, err := p.parseBlockAny(childIndent)
			if err != nil {
				return nil, err
			}
			list = append(list, child)
			continue
		}

		if idx := findYAMLColon(item); idx >= 0 {
			key := strings.TrimSpace(item[:idx])
			rest := strings.TrimSpace(item[idx+1:])
			m := map[string]any{}
			if rest == "" {
				childIndent := p.nextIndent(indent)
				child, err := p.parseBlockAny(childIndent)
				if err != nil {
					return nil, err
				}
				if child == nil {
					child = map[string]any{}
				}
				m[key] = child
			} else {
				m[key] = parseYAMLScalar(rest)
			}
			// 列表项后续同级键并入同一个映射项
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				cont, err := p.parseMap(p.lines[p.pos].indent)
				if err != nil {
					return nil, err
				}
				for k, v := range cont {
					m[k] = v
				}
			}
			list = append(list, m)
		} else {
			list = append(list, parseYAMLScalar(item))
		}
	}
	return list, nil
}

func (p *yparser) nextIndent(indent int) int {
	if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
		return p.lines[p.pos].indent
	}
	return indent + 2
}

func isYAMLListItem(s string) bool {
	return s == "-" || strings.HasPrefix(s, "- ")
}

func findYAMLColon(s string) int {
	var inSingle, inDouble bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == ':' && !inSingle && !inDouble:
			if i+1 >= len(s) || s[i+1] == ' ' || s[i+1] == '\t' {
				return i
			}
		}
	}
	return -1
}

func parseYAMLScalar(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return []any{}
		}
		parts := splitYAMLInline(inner)
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			out = append(out, parseYAMLScalar(strings.TrimSpace(part)))
		}
		return out
	}
	switch strings.ToLower(s) {
	case "true":
		return true
	case "false":
		return false
	case "null", "~":
		return nil
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

func splitYAMLInline(s string) []string {
	var parts []string
	var cur strings.Builder
	var inSingle, inDouble bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			cur.WriteByte(c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			cur.WriteByte(c)
		case c == ',' && !inSingle && !inDouble:
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}
	return parts
}
