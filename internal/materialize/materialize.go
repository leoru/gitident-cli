// Package materialize copies a profile's settings straight into a repository's
// own config file (.git/config), for tools that never evaluate the includeIf
// rules in the global gitconfig: containers that only see the repository,
// libgit2-based clients without hasconfig support, and so on.
//
// gitident owns exactly the keys it wrote. It records them in a [gitident]
// section together with a hash of their values, so a later run can tell its
// own copy from values someone changed by hand, and never overwrites the latter
// without --force.
package materialize

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/leoru/gitident-cli/internal/gitx"
	"github.com/leoru/gitident-cli/internal/render"
)

// Keys of the ownership record in the repository config.
const (
	Section    = "gitident"
	KeyProfile = "gitident.profile"
	KeyHash    = "gitident.hash"
	KeyOwned   = "gitident.key"
)

// Record is what a repository config file says about a materialized profile.
type Record struct {
	Profile string   // "" when nothing is materialized
	Keys    []string // the keys gitident wrote
	Hash    string   // hash of the settings as written
	Current string   // hash of the current values of Keys

	values map[string][]string // canonical key → values in the file
}

// Materialized reports whether the file holds a copy made by gitident.
func (r *Record) Materialized() bool { return r.Profile != "" }

// Edited reports whether a value gitident wrote has changed since.
func (r *Record) Edited() bool { return r.Materialized() && r.Hash != r.Current }

// UpToDate reports whether the file holds exactly settings for profile.
func (r *Record) UpToDate(profile string, settings []render.Setting) bool {
	return r.Profile == profile && !r.Edited() && r.Hash == Hash(Normalize(settings))
}

// Differences lists the settings whose value in the file is not the wanted
// one, as "key = current value" ("key unset" when absent).
func (r *Record) Differences(settings []render.Setting) []string {
	var out []string
	for _, s := range Normalize(settings) {
		cur := r.values[render.CanonicalKey(s.Key)]
		switch {
		case len(cur) == 0:
			out = append(out, s.Key+" unset")
		case len(cur) > 1 || cur[0] != s.Value:
			out = append(out, s.Key+" = "+cur[len(cur)-1])
		}
	}
	return out
}

// Conflicts lists settings gitident does not own that the file already sets
// to a different value; writing would overwrite them.
func (r *Record) Conflicts(settings []render.Setting) []string {
	owned := map[string]bool{}
	for _, k := range r.Keys {
		owned[render.CanonicalKey(k)] = true
	}
	var out []string
	for _, s := range Normalize(settings) {
		ck := render.CanonicalKey(s.Key)
		cur := r.values[ck]
		if owned[ck] || len(cur) == 0 || (len(cur) == 1 && cur[0] == s.Value) {
			continue
		}
		out = append(out, s.Key+" = "+cur[len(cur)-1])
	}
	return out
}

// Read parses the materialization record in a config file.
func Read(file string) (*Record, error) {
	entries, err := gitx.ListFile(file)
	if err != nil {
		return nil, err
	}
	r := &Record{values: map[string][]string{}}
	for _, e := range entries {
		r.values[e.Key] = append(r.values[e.Key], e.Value)
	}
	r.Profile = last(r.values[KeyProfile])
	r.Hash = last(r.values[KeyHash])
	r.Keys = r.values[KeyOwned]
	var cur []render.Setting
	for _, k := range r.Keys {
		for _, v := range r.values[render.CanonicalKey(k)] {
			cur = append(cur, render.Setting{Key: k, Value: v})
		}
	}
	r.Current = Hash(cur)
	return r, nil
}

// Hash fingerprints settings: keys (canonicalised) and values, in order.
func Hash(settings []render.Setting) string {
	h := sha256.New()
	for _, s := range settings {
		h.Write([]byte(render.CanonicalKey(s.Key) + "=" + s.Value + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Normalize drops repeated keys, keeping the last value at the first key's
// position, since a single config value is all a key can hold after writing.
func Normalize(settings []render.Setting) []render.Setting {
	pos := map[string]int{}
	var out []render.Setting
	for _, s := range settings {
		ck := render.CanonicalKey(s.Key)
		if i, ok := pos[ck]; ok {
			out[i].Value = s.Value
			continue
		}
		pos[ck] = len(out)
		out = append(out, s)
	}
	return out
}

// Action is what Apply or Remove did (or, in a dry run, would do).
type Action int

const (
	Unchanged Action = iota // already current, or nothing to remove
	Written                 // values written or updated
	Removed                 // values removed
	Edited                  // skipped: values were changed by hand
	Conflict                // skipped: unowned keys are set to other values
)

// Result describes one Apply or Remove.
type Result struct {
	Action   Action
	Previous string   // profile materialized before
	Details  []string // Edited: differing keys; Conflict: conflicting keys
	Record   *Record  // the record as read before any change
}

// Options control Apply and Remove.
type Options struct {
	Force  bool // overwrite edited or conflicting values
	DryRun bool // report without writing
}

// Apply makes file hold profile's settings.
func Apply(file, profile string, settings []render.Setting, opts Options) (Result, error) {
	settings = Normalize(settings)
	rec, err := Read(file)
	if err != nil {
		return Result{}, err
	}
	res := Result{Previous: rec.Profile, Record: rec}
	if rec.UpToDate(profile, settings) {
		return res, nil
	}
	if !opts.Force {
		if rec.Edited() {
			if diff := rec.Differences(settings); len(diff) > 0 {
				res.Action, res.Details = Edited, diff
				return res, nil
			}
		}
		if c := rec.Conflicts(settings); len(c) > 0 {
			res.Action, res.Details = Conflict, c
			return res, nil
		}
	}
	res.Action = Written
	if opts.DryRun {
		return res, nil
	}
	return res, write(file, rec, profile, settings)
}

func write(file string, rec *Record, profile string, settings []render.Setting) error {
	wanted := map[string]bool{}
	for _, s := range settings {
		wanted[render.CanonicalKey(s.Key)] = true
	}
	for _, k := range rec.Keys {
		if !wanted[render.CanonicalKey(k)] {
			if err := gitx.FileUnsetAll(file, k); err != nil {
				return err
			}
		}
	}
	for _, s := range settings {
		if err := gitx.FileSet(file, s.Key, s.Value); err != nil {
			return err
		}
	}
	// The record goes last: an interrupted write then looks edited, which
	// makes the next run stop and ask for --force rather than guess.
	if err := gitx.FileRemoveSection(file, Section); err != nil {
		return err
	}
	if err := gitx.FileSet(file, KeyProfile, profile); err != nil {
		return err
	}
	if err := gitx.FileSet(file, KeyHash, Hash(settings)); err != nil {
		return err
	}
	for _, s := range settings {
		if err := gitx.FileAdd(file, KeyOwned, s.Key); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes the materialized settings and the record from file.
func Remove(file string, opts Options) (Result, error) {
	rec, err := Read(file)
	if err != nil {
		return Result{}, err
	}
	res := Result{Previous: rec.Profile, Record: rec}
	if !rec.Materialized() {
		return res, nil
	}
	if rec.Edited() && !opts.Force {
		res.Action = Edited
		return res, nil
	}
	res.Action = Removed
	if opts.DryRun {
		return res, nil
	}
	for _, k := range rec.Keys {
		if err := gitx.FileUnsetAll(file, k); err != nil {
			return res, err
		}
	}
	return res, gitx.FileRemoveSection(file, Section)
}

func last(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	return vals[len(vals)-1]
}
