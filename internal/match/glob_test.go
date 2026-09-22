package match

import "testing"

func TestGlob(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"git@github.com:company/**", "git@github.com:company/infra.git", true},
		{"git@github.com:company/**", "git@github.com:company/group/infra.git", true},
		{"git@github.com:company/*", "git@github.com:company/group/infra.git", false},
		{"git@github.com:company/*", "git@github.com:company/infra.git", true},
		{"git@github.com:company/**", "git@github.com:other/infra.git", false},
		{"https://gitlab.company.com/**", "https://gitlab.company.com/a/b/c.git", true},
		// "**" not bounded by "/" acts like "*".
		{"https://gitlab.company.com**", "https://gitlab.company.com/a", false},
		{"git@github.com:**", "git@github.com:a/b", false},
		{"git@github.com:**", "git@github.com:ab", true},
		{"/home/k/work/**", "/home/k/work/api/.git", true},
		{"/home/k/work/**", "/home/k/workshop/api/.git", false},
		{"**/api/.git", "/home/k/work/api/.git", true},
		{"/home/**/.git", "/home/.git", true},
		{"/home/**/.git", "/home/a/b/.git", true},
		{"/home/k/*/x/.git", "/home/k/work/x/.git", true},
		{"/home/k/*/x/.git", "/home/k/a/b/x/.git", false},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{"[ab]x", "bx", true},
		{"[!ab]x", "bx", false},
		{"[a-c]x", "cx", true},
		{"[]]x", "]x", true},
		{`a\*b`, "a*b", true},
		{`a\*b`, "axb", false},
		{"exact", "exact", true},
		{"exact", "exactly", false},
		{"", "", true},
		{"[unterminated", "[unterminated", true},
		{"https://github.com/[[:alpha:]]cme/*", "https://github.com/acme/x", true},
		{"[[:digit:]]x", "7x", true},
		{"[[:digit:]]x", "ax", false},
		{"[![:upper:]]", "a", true},
	}
	for _, c := range cases {
		if got := Glob(c.pat, c.s); got != c.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", c.pat, c.s, got, c.want)
		}
	}
}
