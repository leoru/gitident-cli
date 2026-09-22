package config

import (
	"errors"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// Document is a profiles.yaml file loaded as a yaml.Node tree, so that it can be
// edited programmatically while keeping the user's comments and key order.
type Document struct {
	root *yaml.Node
}

// ParseDocument parses data for editing.
func ParseDocument(data []byte) (*Document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("config is not a YAML mapping")
	}
	return &Document{root: &root}, nil
}

// Bytes re-encodes the document.
func (d *Document) Bytes() ([]byte, error) { return Marshal(d.root) }

func (d *Document) top() *yaml.Node { return d.root.Content[0] }

// mapGet returns the value node for key in a mapping node, or nil.
func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mapEnsure returns the value node for key, creating it with the given kind if absent.
func mapEnsure(m *yaml.Node, key string, kind yaml.Kind) *yaml.Node {
	if v := mapGet(m, key); v != nil {
		if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
			v.Kind, v.Tag, v.Value = kind, "", ""
		}
		return v
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	v := &yaml.Node{Kind: kind}
	m.Content = append(m.Content, k, v)
	return v
}

func (d *Document) profileNode(name string) (*yaml.Node, error) {
	profiles := mapGet(d.top(), "profiles")
	if profiles == nil || profiles.Kind != yaml.MappingNode {
		return nil, errors.New("config has no `profiles` mapping")
	}
	p := mapGet(profiles, name)
	if p == nil {
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	if p.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("profile %q is not a mapping", name)
	}
	return p, nil
}

// AddRepo appends repo to profiles.<profile>.repos unless an equivalent entry is
// already there. It reports whether the document changed.
func (d *Document) AddRepo(profile, repo string) (bool, error) {
	p, err := d.profileNode(profile)
	if err != nil {
		return false, err
	}
	repos := mapEnsure(p, "repos", yaml.SequenceNode)
	if repos.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("profile %q: `repos` is not a list", profile)
	}
	key := RepoKey(repo)
	for _, n := range repos.Content {
		if n.Value == repo || (key != "" && RepoKey(n.Value) == key) {
			return false, nil
		}
	}
	repos.Content = append(repos.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: repo})
	return true, nil
}

// RemoveRepo removes entries equivalent to repo from every profile except keep.
// It returns the profiles it removed the repo from.
func (d *Document) RemoveRepo(repo, keep string) []string {
	profiles := mapGet(d.top(), "profiles")
	if profiles == nil || profiles.Kind != yaml.MappingNode {
		return nil
	}
	key := RepoKey(repo)
	var removed []string
	for i := 0; i+1 < len(profiles.Content); i += 2 {
		name := profiles.Content[i].Value
		if name == keep || profiles.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		repos := mapGet(profiles.Content[i+1], "repos")
		if repos == nil || repos.Kind != yaml.SequenceNode {
			continue
		}
		kept := repos.Content[:0]
		for _, n := range repos.Content {
			if n.Value == repo || (key != "" && RepoKey(n.Value) == key) {
				removed = append(removed, name)
				continue
			}
			kept = append(kept, n)
		}
		repos.Content = kept
	}
	return removed
}

// AddProfile appends a new profile with an optional head comment.
func (d *Document) AddProfile(name string, p *Profile, comment string) error {
	profiles := mapEnsure(d.top(), "profiles", yaml.MappingNode)
	if mapGet(profiles, name) != nil {
		return fmt.Errorf("profile %q already exists", name)
	}
	var v yaml.Node
	if err := v.Encode(p); err != nil {
		return err
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Value: name, HeadComment: comment}
	profiles.Content = append(profiles.Content, k, &v)
	return nil
}

// AddRule appends a rule at the end (highest rule precedence).
func (d *Document) AddRule(r Rule) error {
	rules := mapEnsure(d.top(), "rules", yaml.SequenceNode)
	if rules.Kind != yaml.SequenceNode {
		return errors.New("`rules` is not a list")
	}
	var v yaml.Node
	if err := v.Encode(r); err != nil {
		return err
	}
	rules.Content = append(rules.Content, &v)
	return nil
}

// PrependRules inserts rules at the start of `rules`, i.e. with the lowest
// precedence, keeping their relative order.
func (d *Document) PrependRules(rs []Rule) error {
	rules := mapEnsure(d.top(), "rules", yaml.SequenceNode)
	if rules.Kind != yaml.SequenceNode {
		return errors.New("`rules` is not a list")
	}
	nodes := make([]*yaml.Node, 0, len(rs)+len(rules.Content))
	for _, r := range rs {
		var v yaml.Node
		if err := v.Encode(r); err != nil {
			return err
		}
		nodes = append(nodes, &v)
	}
	rules.Content = append(nodes, rules.Content...)
	return nil
}

// SetExtra sets profiles.<profile>.extra.<key> = value.
func (d *Document) SetExtra(profile, key, value string) error {
	p, err := d.profileNode(profile)
	if err != nil {
		return err
	}
	extra := mapEnsure(p, "extra", yaml.MappingNode)
	if extra.Kind != yaml.MappingNode {
		return fmt.Errorf("profile %q: `extra` is not a mapping", profile)
	}
	if v := mapGet(extra, key); v != nil {
		v.Kind, v.Tag, v.Value = yaml.ScalarNode, "!!str", value
		return nil
	}
	extra.Content = append(extra.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
