package cache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A key changes with the key files' content and the --with strings, not
// with anything else; a key is written once; a restore never replaces a
// tree that's there, and brings back files, modes and links.
func TestSnapshots(t *testing.T) {
	root, base := t.TempDir(), t.TempDir()
	r, err := Open(base, "repo")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "lock.json"), []byte("v1"), 0o644)
	k1 := SnapKey(root, "deps", []string{"lock.json"}, nil)
	if SnapKey(root, "deps", []string{"lock.json"}, nil) != k1 {
		t.Fatal("the key isn't stable")
	}
	if SnapKey(root, "deps", []string{"lock.json"}, []string{"node v22"}) == k1 || SnapKey(root, "other", []string{"lock.json"}, nil) == k1 {
		t.Error("--with or the tree doesn't change the key")
	}
	os.WriteFile(filepath.Join(root, "lock.json"), []byte("v2"), 0o644)
	if SnapKey(root, "deps", []string{"lock.json"}, nil) == k1 {
		t.Error("the key file's content doesn't change the key")
	}

	tree := filepath.Join(root, "deps")
	os.MkdirAll(filepath.Join(tree, "bin"), 0o755)
	os.WriteFile(filepath.Join(tree, "bin", "tool"), []byte("#!/bin/sh\n"), 0o755)
	os.Symlink("bin/tool", filepath.Join(tree, "tool"))
	s, err := r.SaveSnap(root, Snapshot{Key: k1, Tree: "deps", SavedBy: "zach"})
	if err != nil || s.Files != 1 || s.TreeSHA256 == "" {
		t.Fatalf("save: %v %+v", err, s)
	}
	again, err := r.SaveSnap(root, Snapshot{Key: k1, Tree: "deps", SavedBy: "someone else"})
	if err != nil || again.SavedBy != "zach" {
		t.Errorf("a second save of the key: %v %+v (written once)", err, again)
	}
	if r.FindSnap("deps", "nope") != nil {
		t.Error("a missing key was found")
	}
	if err := r.RestoreSnap(root, s); !errors.Is(err, ErrTreeExists) {
		t.Errorf("restore over a tree that's there: %v", err)
	}
	os.RemoveAll(tree)
	if err := r.RestoreSnap(root, r.FindSnap("deps", k1)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(tree, "bin", "tool")); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("restored file: %v %v", fi, err)
	}
	if target, err := os.Readlink(filepath.Join(tree, "tool")); err != nil || target != "bin/tool" {
		t.Errorf("restored link: %q %v", target, err)
	}
	if h, _, _, _ := treeHash(tree); h != s.TreeSHA256 {
		t.Error("the restored tree hashes differently")
	}
}

func TestSnapDecl(t *testing.T) {
	decls, problems := ParseDecl("npm @package-lock.json snap:web/node_modules @web/package-lock.json")
	if len(problems) != 0 || len(decls) != 2 || decls[1].Tree != "web/node_modules" || decls[1].Name() != "snap.web_node_modules" || decls[1].Label() != "snap:web/node_modules" {
		t.Fatalf("%v %v", decls, problems)
	}
	if _, problems := ParseDecl("snap:../out @x"); len(problems) == 0 {
		t.Error("a tree outside the repo was taken")
	}
	if _, problems := ParseDecl("snap:vendor"); len(problems) == 0 {
		t.Error("a snapshot with no key files was taken")
	}
}
