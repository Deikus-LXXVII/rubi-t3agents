package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Core installs a sideloaded plugin from <source>/latest/ and checks updates under <source>/<version>/
// (internal/plugins ReleaseBase in core): the dist script must lay out both, with the same files, and the
// manifest's version must name the folder.
func TestBuildDistLaysOutVersionAndLatest(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the plugin")
	}
	out := t.TempDir()
	cmd := exec.Command("sh", "scripts/build-dist.sh", "v9.8.7-test.1", out)
	cmd.Env = append(os.Environ(), "TARGETS=linux/amd64")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build-dist: %v\n%s", err, b)
	}
	files := []string{"rubi-plugin.json", "t3agents-linux-amd64.tar.gz", "SHA256SUMS"}
	for _, d := range []string{"v9.8.7-test.1", "latest"} {
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(out, d, f)); err != nil {
				t.Errorf("%s/%s is missing", d, f)
			}
		}
	}
	for _, f := range files {
		a, _ := os.ReadFile(filepath.Join(out, "v9.8.7-test.1", f))
		b, _ := os.ReadFile(filepath.Join(out, "latest", f))
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs between the version folder and latest", f)
		}
	}
	var m struct{ Version string }
	raw, _ := os.ReadFile(filepath.Join(out, "latest", "rubi-plugin.json"))
	if json.Unmarshal(raw, &m) != nil || m.Version != "v9.8.7-test.1" {
		t.Errorf("manifest version = %q", m.Version)
	}
	sums, _ := os.ReadFile(filepath.Join(out, "latest", "SHA256SUMS"))
	for _, f := range files[:2] {
		if !bytes.Contains(sums, []byte(f)) {
			t.Errorf("SHA256SUMS does not list %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "latest", "SHA256SUMS.sig")); err == nil {
		t.Error("the script signed something; signing is the key holder's step")
	}
}
