// Package render turns profiles.yaml into gitconfig text: one fragment per profile
// plus the managed block of includeIf rules spliced into the global gitconfig.
package render

import (
	"sort"
	"strings"
)

// entry is one "key = value" line.
type entry struct{ key, value string }

// section is one [section "subsection"] with its entries in output order.
type section struct {
	name, sub string
	entries   []entry
	comment   string // optional comment line emitted before the header
}

func (s *section) add(key, value string) { s.entries = append(s.entries, entry{key, value}) }

// header renders the section header, e.g. [remote "origin"].
func (s *section) header() string {
	if s.sub == "" {
		return "[" + s.name + "]"
	}
	return "[" + s.name + " " + quoteSubsection(s.sub) + "]"
}

func writeSections(b *strings.Builder, sections []*section) {
	for _, s := range sections {
		if s.comment != "" {
			b.WriteString(s.comment)
			b.WriteByte('\n')
		}
		b.WriteString(s.header())
		b.WriteByte('\n')
		for _, e := range s.entries {
			b.WriteString("\t")
			b.WriteString(e.key)
			b.WriteString(" = ")
			b.WriteString(QuoteValue(e.value))
			b.WriteByte('\n')
		}
	}
}

// SplitKey splits a git config key "section.sub.section.key" into its parts.
// The subsection may itself contain dots.
func SplitKey(k string) (sectionName, sub, key string) {
	first := strings.IndexByte(k, '.')
	last := strings.LastIndexByte(k, '.')
	if first < 0 {
		return k, "", ""
	}
	sectionName, key = k[:first], k[last+1:]
	if first != last {
		sub = k[first+1 : last]
	}
	return sectionName, sub, key
}

// quoteSubsection quotes a subsection name as git requires.
func quoteSubsection(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// QuoteValue returns v in a form git reads back verbatim. Values containing
// whitespace, comment characters, quotes or backslashes are double-quoted with
// escapes; plain values are left as-is.
func QuoteValue(v string) string {
	if v == "" {
		return `""`
	}
	if !strings.ContainsAny(v, " \t\n#;\"\\") {
		return v
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// sectionSet keeps sections in insertion order and merges keys into existing ones.
type sectionSet struct {
	list  []*section
	index map[string]*section
}

func (ss *sectionSet) get(name, sub string) *section {
	if ss.index == nil {
		ss.index = map[string]*section{}
	}
	id := strings.ToLower(name) + "\x00" + sub
	if s, ok := ss.index[id]; ok {
		return s
	}
	s := &section{name: name, sub: sub}
	ss.index[id] = s
	ss.list = append(ss.list, s)
	return s
}

// addExtra appends free-form "section[.sub].key" entries: keys for sections that
// already exist are appended to them (sorted), new sections are appended sorted.
func (ss *sectionSet) addExtra(extra map[string]string) {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		si, subi, ki := SplitKey(keys[i])
		sj, subj, kj := SplitKey(keys[j])
		if !strings.EqualFold(si, sj) {
			return strings.ToLower(si) < strings.ToLower(sj)
		}
		if subi != subj {
			return subi < subj
		}
		return ki < kj
	})
	for _, k := range keys {
		name, sub, key := SplitKey(k)
		ss.get(name, sub).add(key, extra[k])
	}
}
