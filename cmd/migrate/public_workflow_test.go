package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPublicWorkflowUsesSafePinnedActions(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Steps []struct {
				Uses string         `yaml:"uses"`
				Run  string         `yaml:"run"`
				With map[string]any `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.Permissions) != 1 || workflow.Permissions["contents"] != "read" {
		t.Fatal("CI must have read-only repository permissions")
	}
	pinned := regexp.MustCompile(`^[^@]+@[a-f0-9]{40}$`)
	seenDefault := false
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.Uses != "" && !pinned.MatchString(step.Uses) {
				t.Error("CI action is not pinned to a full commit")
			}
			if strings.HasPrefix(step.Uses, "actions/checkout@") && step.With["persist-credentials"] != false {
				t.Error("checkout must not persist credentials")
			}
			if strings.Contains(step.Run, "go test ./...") {
				seenDefault = true
			}
			if strings.Contains(step.Run, "-tags integration") || strings.Contains(step.Run, "git push") {
				t.Error("default CI must not run live DB integration or publish")
			}
		}
	}
	if !seenDefault || strings.Contains(string(data), "pull_request_target:") {
		t.Fatal("unsafe or missing default CI test trigger")
	}
}
