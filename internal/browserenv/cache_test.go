package browserenv

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestProfileCloseReclaimsCachesAndRetainsWebsiteState(t *testing.T) {
	e, _ := New()
	p, err := Open(t.Context(), t.TempDir(), e)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	caches := []string{"Default/Cache", "Default/Code Cache", "GPUPersistentCache", "Default/GPUCache", "Default/DawnWebGPUCache", "Default/DawnGraphiteCache"}
	retained := []string{"Default/Cookies", "Default/Cookies-wal", "Default/Local Storage/leveldb/data", "Default/IndexedDB/data", "Default/Session Storage/data", "Default/Service Worker/CacheStorage/data", "Default/Preferences", "Local State", "unknown-cache/data"}
	for _, name := range append(append([]string{}, retained...), caches...) {
		path := filepath.Join(p.Directory, name)
		if slices.Contains(caches, name) {
			path = filepath.Join(path, "entry")
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("retained-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if other, err := Open(ctx, filepath.Dir(p.Directory), e); err == nil {
		other.Close()
		t.Fatal("active profile admitted another writer")
	}
	for _, name := range caches {
		if _, err := os.Stat(filepath.Join(p.Directory, name, "entry")); err != nil {
			t.Fatal("cache removed during the active attempt", err)
		}
	}
	p.Close()
	for _, name := range caches {
		if _, err := os.Lstat(filepath.Join(p.Directory, name)); !os.IsNotExist(err) {
			t.Errorf("idle profile retained rebuildable cache %s: %v", name, err)
		}
	}
	for _, name := range retained {
		raw, err := os.ReadFile(filepath.Join(p.Directory, name))
		if err != nil || string(raw) != "retained-"+name {
			t.Errorf("persistent state changed: %s: %v", name, err)
		}
	}
	recipe, err := os.ReadFile(p.RecipePath)
	if err != nil || string(recipe) != string(e.Recipe) {
		t.Fatal("fixed fingerprint recipe changed", err)
	}
}

func TestProfileOpenRecoversInterruptedCacheCleanup(t *testing.T) {
	e, _ := New()
	root := t.TempDir()
	directory := filepath.Join(root, e.ProfileID.String())
	path := filepath.Join(directory, "Default", "Code Cache", "entry")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old cache"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Open(t.Context(), root, e)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("recovered profile retained interrupted cache", err)
	}
}

func TestProfileCacheCleanupNeverFollowsExternalLinks(t *testing.T) {
	for _, linked := range []string{"Default", "Default/Cache", "Default/Code Cache/nested"} {
		t.Run(linked, func(t *testing.T) {
			e, _ := New()
			root := t.TempDir()
			directory := filepath.Join(root, e.ProfileID.String())
			external := t.TempDir()
			protected := filepath.Join(external, "protected")
			if err := os.WriteFile(protected, []byte("other account"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(directory, linked)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, link); err != nil {
				t.Fatal(err)
			}
			p, err := Open(t.Context(), root, e)
			if linked == "Default" {
				if err == nil {
					p.Close()
					t.Fatal("symlinked profile parent accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				p.Close()
			}
			if raw, err := os.ReadFile(protected); err != nil || string(raw) != "other account" {
				t.Fatal("cleanup changed linked external state", err)
			}
		})
	}
}
