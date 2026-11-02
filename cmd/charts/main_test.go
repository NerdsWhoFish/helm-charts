package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const chart = "apiVersion: v2\nname: fjord\nversion: 1.0.0\nsources:\n- https://github.com/NerdsWhoFish/fjord\n"

type fixtureEntry struct {
	name, body string
	mode       int64
	kind       byte
}

func archiveFixture(t *testing.T, filename string, entries []fixtureEntry, stamp time.Time) string {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	gz.ModTime = stamp
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0644
		}
		h := &tar.Header{Name: e.name, Mode: mode, Size: int64(len(e.body)), Typeflag: kind, ModTime: stamp}
		if kind == tar.TypeSymlink {
			h.Linkname = "/etc/passwd"
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind != tar.TypeSymlink {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(file, buffer.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestInspectRejectsUnsafeArchives(t *testing.T) {
	good := []fixtureEntry{{name: "fjord/Chart.yaml", body: chart}}
	tests := map[string][]fixtureEntry{
		"traversal":       append(append([]fixtureEntry{}, good...), fixtureEntry{name: "fjord/../outside", body: "bad"}),
		"absolute":        append(append([]fixtureEntry{}, good...), fixtureEntry{name: "/outside", body: "bad"}),
		"link":            append(append([]fixtureEntry{}, good...), fixtureEntry{name: "fjord/link", kind: tar.TypeSymlink}),
		"duplicate":       append(append([]fixtureEntry{}, good...), good...),
		"other-root":      append(append([]fixtureEntry{}, good...), fixtureEntry{name: "other/values.yaml"}),
		"invalid-version": {{name: "fjord/Chart.yaml", body: strings.Replace(chart, "1.0.0", "../bad", 1)}},
		"missing-chart":   {{name: "fjord/values.yaml"}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := inspect(archiveFixture(t, "fjord-1.0.0.tgz", entries, time.Time{})); err == nil {
				t.Fatal("accepted unsafe archive")
			}
		})
	}
	t.Run("wrong-filename", func(t *testing.T) {
		if _, err := inspect(archiveFixture(t, "fjord-2.0.0.tgz", good, time.Time{})); err == nil {
			t.Fatal("accepted filename mismatch")
		}
	})
	t.Run("corrupt-gzip", func(t *testing.T) {
		file := archiveFixture(t, "fjord-1.0.0.tgz", good, time.Time{})
		b, _ := os.ReadFile(file)
		b[len(b)-8] ^= 1
		if err := os.WriteFile(file, b, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := inspect(file); err == nil {
			t.Fatal("accepted invalid gzip checksum")
		}
	})
}

func TestImportPreservesVersionAcrossTimestampRetries(t *testing.T) {
	root := t.TempDir()
	allowed := map[string]string{"fjord": "NerdsWhoFish/fjord"}
	entries := []fixtureEntry{{name: "fjord/Chart.yaml", body: chart}, {name: "fjord/values.yaml", body: "replicas: 1\n"}}
	first := archiveFixture(t, "fjord-1.0.0.tgz", entries, time.Unix(100, 0))
	retry := archiveFixture(t, "fjord-1.0.0.tgz", entries, time.Unix(200, 0))
	if err := importArchive(root, first, "NerdsWhoFish/fjord", allowed); err != nil {
		t.Fatal(err)
	}
	if err := importArchive(root, retry, "NerdsWhoFish/fjord", allowed); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(first)
	got, _ := os.ReadFile(filepath.Join(root, "packages/fjord/fjord-1.0.0.tgz"))
	if !bytes.Equal(want, got) {
		t.Fatal("retry replaced original archive bytes")
	}
	entries[1].body = "replicas: 2\n"
	changed := archiveFixture(t, "fjord-1.0.0.tgz", entries, time.Unix(200, 0))
	if err := importArchive(root, changed, "NerdsWhoFish/fjord", allowed); err == nil {
		t.Fatal("accepted changed content for same version")
	}
	if err := importArchive(root, retry, "Other/project", allowed); err == nil {
		t.Fatal("accepted wrong publisher")
	}
}

func TestCollectRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "packages"), 0755); err != nil {
		t.Fatal(err)
	}
	file := archiveFixture(t, "fjord-1.0.0.tgz", []fixtureEntry{{name: "fjord/Chart.yaml", body: chart}}, time.Time{})
	if err := os.Symlink(file, filepath.Join(root, "packages", "link.tgz")); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(root, map[string]string{"fjord": "NerdsWhoFish/fjord"}); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestImmutableRejectsRemovalAndReplacement(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	target := filepath.Join(root, "packages", "fjord", "fjord-1.0.0.tgz")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "initial")
	base := git("rev-parse", "HEAD")
	if err := immutable(root, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := immutable(root, base); err == nil {
		t.Fatal("accepted replacement")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := immutable(root, base); err == nil {
		t.Fatal("accepted removal")
	}
}

func TestAuthorizeRequiresSourceMetadata(t *testing.T) {
	if err := authorize(metadata{Name: "fjord"}, "NerdsWhoFish/fjord", map[string]string{"fjord": "NerdsWhoFish/fjord"}); err == nil {
		t.Fatal("accepted missing source metadata")
	}
}
