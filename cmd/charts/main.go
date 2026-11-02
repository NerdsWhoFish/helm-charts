// charts validates immutable chart archives before they enter the public repository.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	maxArchive  = 10 << 20
	maxExpanded = 50 << 20
	maxFiles    = 2000
)

var (
	chartName  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	version    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
	repository = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	revision   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type metadata struct {
	APIVersion string   `yaml:"apiVersion"`
	Name       string   `yaml:"name"`
	Version    string   `yaml:"version"`
	Sources    []string `yaml:"sources"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(os.Args[1:]); err != nil {
		slog.Error("chart operation failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	f := flag.NewFlagSet("charts", flag.ContinueOnError)
	root := f.String("root", ".", "repository directory")
	archive := f.String("import", "", "archive to import")
	source := f.String("source", "", "publishing GitHub repository")
	base := f.String("base", "", "previous Git commit to enforce immutability")
	output := f.String("output", "", "copy validated packages into this empty directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	publishers, err := loadPublishers(filepath.Join(*root, "publishers.json"))
	if err != nil {
		return err
	}
	if *archive != "" {
		if _, err := collect(*root, publishers); err != nil {
			return err
		}
		if err := importArchive(*root, *archive, *source, publishers); err != nil {
			return err
		}
	}
	archives, err := collect(*root, publishers)
	if err != nil {
		return err
	}
	if *base != "" && *base != strings.Repeat("0", 40) {
		if err := immutable(*root, *base); err != nil {
			return err
		}
	}
	if *output != "" {
		if entries, err := os.ReadDir(*output); err == nil && len(entries) != 0 {
			return errors.New("output directory must be empty")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(*output, 0755); err != nil {
			return err
		}
		for _, file := range archives {
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(*output, filepath.Base(file)), data, 0644); err != nil {
				return err
			}
		}
	}
	slog.Info("chart validation complete", "packages", len(archives))
	return nil
}

func loadPublishers(file string) (map[string]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var publishers map[string]string
	if err := json.Unmarshal(data, &publishers); err != nil {
		return nil, err
	}
	if len(publishers) == 0 {
		return nil, errors.New("publisher allowlist is empty")
	}
	for name, repo := range publishers {
		if !chartName.MatchString(name) || !repository.MatchString(repo) {
			return nil, errors.New("invalid publisher allowlist")
		}
	}
	return publishers, nil
}

func inspect(file string) (metadata, error) {
	var meta metadata
	info, err := os.Lstat(file)
	if err != nil {
		return meta, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxArchive {
		return meta, errors.New("archive must be a regular file of at most 10 MiB")
	}
	input, err := os.Open(file)
	if err != nil {
		return meta, err
	}
	defer input.Close()
	gz, err := gzip.NewReader(input)
	if err != nil {
		return meta, err
	}
	defer gz.Close()
	limited := &io.LimitedReader{R: gz, N: maxExpanded + 1}
	tr := tar.NewReader(limited)
	seen := map[string]bool{}
	root := ""
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return meta, err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || len(name) > 240 || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") ||
			strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return meta, errors.New("unsafe archive path")
		}
		parts := strings.Split(name, "/")
		if root == "" {
			root = parts[0]
		}
		if parts[0] != root || !chartName.MatchString(root) {
			return meta, errors.New("archive must contain one chart directory")
		}
		if seen[name] {
			return meta, errors.New("duplicate archive entry")
		}
		seen[name] = true
		if len(seen) > maxFiles {
			return meta, errors.New("archive has too many files")
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return meta, errors.New("archive contains a link or special file")
		}
		if h.Size < 0 || h.Size > maxExpanded {
			return meta, errors.New("archive entry is too large")
		}
		if name == root+"/Chart.yaml" {
			if h.Size > 64<<10 {
				return meta, errors.New("Chart.yaml is too large")
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return meta, err
			}
			if err := yaml.Unmarshal(data, &meta); err != nil {
				return meta, err
			}
			found = true
		}
	}
	// Consume the gzip footer so truncated streams and corrupt checksums fail.
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return meta, err
	}
	if limited.N <= 0 {
		return meta, errors.New("archive expands beyond 50 MiB")
	}
	if !found || meta.APIVersion != "v2" || meta.Name != root || !version.MatchString(meta.Version) {
		return meta, errors.New("invalid chart metadata")
	}
	if filepath.Base(file) != meta.Name+"-"+meta.Version+".tgz" {
		return meta, errors.New("archive filename does not match chart metadata")
	}
	return meta, nil
}

func authorize(meta metadata, source string, publishers map[string]string) error {
	expected, ok := publishers[meta.Name]
	if !ok || (source != "" && !strings.EqualFold(source, expected)) {
		return errors.New("chart publisher is not allowed")
	}
	for _, url := range meta.Sources {
		if url == "https://github.com/"+expected {
			return nil
		}
	}
	return errors.New("chart sources must include the allowed repository URL")
}

func importArchive(root, file, source string, publishers map[string]string) error {
	if !repository.MatchString(source) {
		return errors.New("source repository is required for import")
	}
	meta, err := inspect(file)
	if err != nil {
		return err
	}
	if err := authorize(meta, source, publishers); err != nil {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	target := filepath.Join(root, "packages", meta.Name, filepath.Base(file))
	// Packaging timestamps may change on retry; retain the original bytes.
	if prior, err := os.ReadFile(target); err == nil {
		if !bytes.Equal(prior, data) {
			original, err := contentDigest(target)
			if err != nil {
				return err
			}
			incoming, err := contentDigest(file)
			if err != nil {
				return err
			}
			if original != incoming {
				return errors.New("published chart version cannot be overwritten")
			}
		}
		slog.Info("chart already present", "chart", meta.Name, "version", meta.Version)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(target, data, 0644); err != nil {
		return err
	}
	slog.Info("chart imported", "chart", meta.Name, "version", meta.Version)
	return nil
}

func contentDigest(file string) ([32]byte, error) {
	var empty [32]byte
	if _, err := inspect(file); err != nil {
		return empty, err
	}
	f, err := os.Open(file)
	if err != nil {
		return empty, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return empty, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	type entry struct {
		Mode   int64
		Type   byte
		Digest [32]byte
	}
	entries := map[string]entry{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return empty, err
		}
		sum := sha256.New()
		if _, err := io.Copy(sum, tr); err != nil {
			return empty, err
		}
		var digest [32]byte
		copy(digest[:], sum.Sum(nil))
		entries[strings.TrimSuffix(h.Name, "/")] = entry{h.Mode, h.Typeflag, digest}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return empty, err
	}
	return sha256.Sum256(data), nil
}

func collect(root string, publishers map[string]string) ([]string, error) {
	dir := filepath.Join(root, "packages")
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var archives []string
	err := filepath.WalkDir(dir, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		meta, err := inspect(file)
		if err != nil {
			return fmt.Errorf("validate %s: %w", filepath.Base(file), err)
		}
		if err := authorize(meta, "", publishers); err != nil {
			return err
		}
		if filepath.Dir(file) != filepath.Join(dir, meta.Name) {
			return errors.New("package is not in its chart directory")
		}
		archives = append(archives, file)
		return nil
	})
	return archives, err
}

func immutable(root, base string) error {
	if !revision.MatchString(base) {
		return errors.New("base must be a full Git commit SHA")
	}
	cmd := exec.Command("git", "-C", root, "ls-tree", "-r", "--name-only", "-z", base, "--", "packages/")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read previous package tree: %w", err)
	}
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" {
			continue
		}
		if path.Clean(name) != name || !strings.HasPrefix(name, "packages/") {
			return errors.New("invalid historical package path")
		}
		prior, err := exec.Command("git", "-C", root, "show", base+":"+name).Output()
		if err != nil {
			return err
		}
		current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return errors.New("published chart cannot be removed")
		}
		if sha256.Sum256(prior) != sha256.Sum256(current) {
			return errors.New("published chart cannot be changed")
		}
	}
	return nil
}
