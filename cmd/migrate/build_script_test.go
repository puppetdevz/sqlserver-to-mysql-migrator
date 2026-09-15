package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildScriptAllPlatformsAndExitStatus(t *testing.T) {
	source, err := os.ReadFile("../../scripts/build.sh")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.MkdirAll(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "build.sh")
	if err = os.WriteFile(script, source, 0700); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(root, "tools")
	if err = os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{
		"go":        "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = -o ]; then shift; printf test > \"$1\"; exit 0; fi; shift; done\nexit 1\n",
		"strip":     "#!/bin/sh\nexit 0\n",
		"sha256sum": "#!/bin/sh\nprintf 'fakehash  %s\\n' \"$1\"\n",
	}
	for name, body := range stubs {
		if err = os.WriteFile(filepath.Join(tools, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", script, "all")
	cmd.Env = append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build stopped before all platforms: %v\n%s", err, output)
	}
	for _, name := range []string{"sqlserver-to-mysql-migrator-darwin-arm64", "sqlserver-to-mysql-migrator-linux-amd64", "sqlserver-to-mysql-migrator-linux-arm64"} {
		if _, err = os.Stat(filepath.Join(root, "dist", name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
	if !strings.Contains(string(output), "3/3") {
		t.Fatalf("missing completion: %s", output)
	}
}
