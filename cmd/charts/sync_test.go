package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncUploadsIndexLastAndRejectsChangedObjects(t *testing.T) {
	script, err := filepath.Abs("../../scripts/sync.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"missing", "different", "denied", "upload-failed"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "dist"), 0755); err != nil {
				t.Fatal(err)
			}
			for file, content := range map[string]string{
				"dist/fjord-1.0.0.tgz": "archive",
				"dist/index.yaml":      "index",
				"index.html":           "page",
			} {
				if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			mock := `#!/usr/bin/env bash
set -eu
case "$*" in
  *"s3api head-object"*)
    case "$SCENARIO" in
      missing|upload-failed) echo "An error occurred (404)" >&2; exit 1;;
      different) echo '{"Metadata":{"sha256":"different"}}';;
      denied) echo "An error occurred (403)" >&2; exit 1;;
    esac;;
  *"s3 cp dist/fjord-1.0.0.tgz"*)
    if test "$SCENARIO" = upload-failed; then exit 1; fi
    echo archive >> "$UPLOAD_LOG";;
  *"s3 cp index.html"*) echo page >> "$UPLOAD_LOG";;
  *"s3 cp dist/index.yaml"*) echo index >> "$UPLOAD_LOG";;
  *) echo unexpected >&2; exit 1;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(mock), 0755); err != nil {
				t.Fatal(err)
			}
			logfile := filepath.Join(dir, "uploads")
			cmd := exec.Command("bash", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"R2_ENDPOINT=https://test.invalid", "R2_BUCKET=test",
				"AWS_ACCESS_KEY_ID=test", "AWS_SECRET_ACCESS_KEY=test",
				"SCENARIO="+scenario, "UPLOAD_LOG="+logfile)
			out, err := cmd.CombinedOutput()
			if scenario == "missing" && err != nil {
				t.Fatalf("%s: %v", out, err)
			}
			if scenario != "missing" && err == nil {
				t.Fatal("failure was ignored")
			}
			uploads, _ := os.ReadFile(logfile)
			if scenario == "missing" {
				if string(uploads) != "archive\npage\nindex\n" {
					t.Fatalf("wrong order: %q", uploads)
				}
			} else if strings.Contains(string(uploads), "index") {
				t.Fatal("index published after failure")
			}
		})
	}
}
