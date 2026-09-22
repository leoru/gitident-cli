package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leoru/gitident-cli/internal/paths"
)

// GitKraken stores one JSON document per profile in
// ~/.gitkraken/profiles/<id>/profile. The schema is undocumented and has changed
// between releases, so fields are looked up by a list of candidate names anywhere
// in the document (case-insensitively), first match wins.
var (
	gkNameKeys        = []string{"userName", "gitName", "authorName", "name"}
	gkEmailKeys       = []string{"userEmail", "gitEmail", "authorEmail", "email"}
	gkProfileNameKeys = []string{"profileName", "profile_name"}
	gkSigningKeyKeys  = []string{"gpgSigningKey", "signingKey", "gpgKey", "signingKeyId"}
	gkSignKeys        = []string{"gpgSign", "commitSigning", "signCommits", "gpgSignCommits"}
	gkFormatKeys      = []string{"gpgFormat", "signingFormat"}
)

// readGitKraken adds one cluster per GitKraken profile. Problems are warnings.
func (im *importer) readGitKraken(dir string) {
	files, _ := filepath.Glob(filepath.Join(dir, "profiles", "*", "profile"))
	if len(files) == 0 {
		im.warnf("no GitKraken profiles found in %s", paths.Contract(filepath.Join(dir, "profiles")))
		return
	}
	sort.Strings(files)
	for _, f := range files {
		id, profileName, err := parseGitKrakenProfile(f)
		if err != nil {
			im.warnf("GitKraken profile %s: %v; skipped", paths.Contract(f), err)
			continue
		}
		label := profileName
		if label == "" {
			label = filepath.Base(filepath.Dir(f))
		}
		h := hint{}
		if profileName != "" {
			h = hint{hintGitKraken, slug(profileName)}
		}
		source := fmt.Sprintf("GitKraken profile %q", label)
		// GitKraken usually knows only name and email; attach it to an identity
		// already found in git config when it does not contradict it.
		if c := im.compatibleCluster(id); c != nil {
			if h.name != "" {
				c.hints = append(c.hints, h)
			}
			c.sources = append(c.sources, source)
			continue
		}
		im.add(id, h, source)
	}
}

func parseGitKrakenProfile(path string) (identity, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return identity{}, "", err
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return identity{}, "", fmt.Errorf("invalid JSON: %w", err)
	}
	fields := map[string]any{}
	collectFields(doc, fields)
	get := func(keys []string) string {
		for _, k := range keys {
			if v, ok := fields[strings.ToLower(k)]; ok {
				switch t := v.(type) {
				case string:
					if s := strings.TrimSpace(t); s != "" {
						return s
					}
				case bool:
					if t {
						return "true"
					}
					return "false"
				}
			}
		}
		return ""
	}
	id := identity{
		Name:       get(gkNameKeys),
		Email:      get(gkEmailKeys),
		SigningKey: get(gkSigningKeyKeys),
		GPGFormat:  get(gkFormatKeys),
		GPGSign:    get(gkSignKeys),
	}
	if id.GPGSign != "" {
		id.GPGSign = normBool(id.GPGSign)
	}
	if id.Email == "" || !strings.Contains(id.Email, "@") {
		return identity{}, "", fmt.Errorf("no author email found")
	}
	return id, get(gkProfileNameKeys), nil
}

// collectFields flattens a JSON document into lower-cased key → first scalar value,
// visiting shallower keys first so top-level fields win over nested ones.
func collectFields(doc any, out map[string]any) {
	queue := []any{doc}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		switch t := cur.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := t[k]
				switch v.(type) {
				case map[string]any, []any:
					queue = append(queue, v)
				default:
					lk := strings.ToLower(k)
					if _, seen := out[lk]; !seen {
						out[lk] = v
					}
				}
			}
		case []any:
			queue = append(queue, t...)
		}
	}
}

// compatibleCluster returns an existing cluster with the same name and email
// whose other fields do not conflict with id's non-empty ones.
func (im *importer) compatibleCluster(id identity) *cluster {
	agree := func(a, b string) bool { return a == "" || a == b }
	for _, c := range im.clusters {
		if c.id.Name == id.Name && strings.EqualFold(c.id.Email, id.Email) &&
			agree(id.SigningKey, c.id.SigningKey) && agree(id.GPGFormat, c.id.GPGFormat) &&
			agree(id.GPGSign, c.id.GPGSign) && agree(id.SSHCommand, c.id.SSHCommand) {
			return c
		}
	}
	return nil
}
