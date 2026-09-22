package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "file")
	if err := WriteFileAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("new file mode = %v", st.Mode().Perm())
	}
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(p)
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode not preserved: %v", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Errorf("content = %q", b)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestWriteFileAtomicFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-gitconfig")
	link := filepath.Join(dir, ".gitconfig")
	_ = os.WriteFile(target, []byte("old"), 0o644)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(link, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was replaced by a regular file")
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Errorf("target content = %q", b)
	}
}

func TestWriteFileAtomicDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, ".gitconfig")
	if err := os.Symlink("dotfiles/gitconfig", link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(link, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("dangling symlink was replaced by a regular file")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "dotfiles", "gitconfig")); string(b) != "x" {
		t.Errorf("target content = %q", b)
	}
}
