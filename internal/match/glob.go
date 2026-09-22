// Package match mirrors the parts of git's config-include logic gitident needs to
// predict: wildmatch globs (as used by includeIf gitdir: and hasconfig:) and the
// profile a repository is expected to get from profiles.yaml.
package match

import "strings"

// Glob reports whether s matches pattern using git's wildmatch rules with
// WM_PATHNAME, which is what includeIf "gitdir:" and "hasconfig:remote.*.url:" use:
//
//   - "*" matches any run of characters except "/"
//   - "?" matches one character except "/"
//   - "[...]" matches a character class ("!" or "^" negates, ranges allowed)
//   - "**" matches anything including "/" when it forms a whole path component
//     ("**/x", "x/**", "x/**/y"); elsewhere it behaves like "*"
//   - "\" escapes the next character
func Glob(pattern, s string) bool {
	return wildmatch(pattern, 0, s)
}

func wildmatch(pat string, pi int, s string) bool {
	si := 0
	for pi < len(pat) {
		c := pat[pi]
		switch c {
		case '\\':
			if pi+1 < len(pat) {
				pi++
				c = pat[pi]
			}
			if si >= len(s) || s[si] != c {
				return false
			}
			pi++
			si++
		case '?':
			if si >= len(s) || s[si] == '/' {
				return false
			}
			pi++
			si++
		case '[':
			if si >= len(s) || s[si] == '/' {
				return false
			}
			ok, next, valid := matchClass(pat, pi, s[si])
			if !valid {
				// Unterminated class: treat "[" literally.
				if s[si] != '[' {
					return false
				}
				pi++
				si++
				continue
			}
			if !ok {
				return false
			}
			pi = next
			si++
		case '*':
			start := pi
			for pi < len(pat) && pat[pi] == '*' {
				pi++
			}
			double := pi-start >= 2 &&
				(start == 0 || pat[start-1] == '/') &&
				(pi == len(pat) || pat[pi] == '/')
			if double {
				if pi == len(pat) {
					return true
				}
				// "**/": zero or more whole directories.
				rest := pi + 1
				if wildmatch(pat, rest, s[si:]) {
					return true
				}
				for k := si; k < len(s); k++ {
					if s[k] == '/' && wildmatch(pat, rest, s[k+1:]) {
						return true
					}
				}
				return false
			}
			for k := si; k <= len(s); k++ {
				if wildmatch(pat, pi, s[k:]) {
					return true
				}
				if k < len(s) && s[k] == '/' {
					break
				}
			}
			return false
		default:
			if si >= len(s) || s[si] != c {
				return false
			}
			pi++
			si++
		}
	}
	return si == len(s)
}

// matchClass matches ch against the bracket expression starting at pat[pi] == '['.
// It returns whether it matched, the index after the closing ']', and whether the
// class was well-formed.
func matchClass(pat string, pi int, ch byte) (matched bool, next int, valid bool) {
	i := pi + 1
	negate := false
	if i < len(pat) && (pat[i] == '!' || pat[i] == '^') {
		negate = true
		i++
	}
	first := true
	for i < len(pat) {
		c := pat[i]
		if c == ']' && !first {
			return matched != negate, i + 1, true
		}
		first = false
		if c == '[' && i+1 < len(pat) && pat[i+1] == ':' {
			if end := strings.Index(pat[i+2:], ":]"); end >= 0 {
				if posixClass(pat[i+2:i+2+end], ch) {
					matched = true
				}
				i += end + 4
				continue
			}
		}
		if c == '\\' && i+1 < len(pat) {
			i++
			c = pat[i]
		}
		if i+2 < len(pat) && pat[i+1] == '-' && pat[i+2] != ']' {
			hi := pat[i+2]
			if hi == '\\' && i+3 < len(pat) {
				hi = pat[i+3]
				i++
			}
			if c <= ch && ch <= hi {
				matched = true
			}
			i += 3
			continue
		}
		if c == ch {
			matched = true
		}
		i++
	}
	return false, 0, false
}

// posixClass matches ch against a [:name:] character class.
func posixClass(name string, ch byte) bool {
	switch name {
	case "alnum":
		return isAlpha(ch) || isDigit(ch)
	case "alpha":
		return isAlpha(ch)
	case "blank":
		return ch == ' ' || ch == '\t'
	case "cntrl":
		return ch < 32 || ch == 127
	case "digit":
		return isDigit(ch)
	case "graph":
		return ch > 32 && ch < 127
	case "lower":
		return ch >= 'a' && ch <= 'z'
	case "print":
		return ch >= 32 && ch < 127
	case "punct":
		return ch > 32 && ch < 127 && !isAlpha(ch) && !isDigit(ch)
	case "space":
		return ch == ' ' || (ch >= '\t' && ch <= '\r')
	case "upper":
		return ch >= 'A' && ch <= 'Z'
	case "xdigit":
		return isDigit(ch) || (ch|0x20 >= 'a' && ch|0x20 <= 'f')
	}
	return false
}

func isAlpha(ch byte) bool { return ch|0x20 >= 'a' && ch|0x20 <= 'z' }
func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }

// HasGlob reports whether s contains wildmatch metacharacters.
func HasGlob(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*', '?', '[', '\\':
			return true
		}
	}
	return false
}
