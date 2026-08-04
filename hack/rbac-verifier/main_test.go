package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEquivalentPermissionsPass(t *testing.T) {
	t.Parallel()

	generated := writeManifest(t, "generated.yaml", `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: manager-role
rules:
- apiGroups: ["", "apps"]
  resources: ["nodes", "deployments"]
  resourceNames: ["worker", "controller"]
  verbs: ["watch", "get"]
- nonResourceURLs: ["/metrics", "/healthz"]
  verbs: ["get"]
`)
	rendered := writeManifest(t, "rendered.yaml", `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: chart-controller-role
  labels:
    app.kubernetes.io/managed-by: Helm
rules:
- nonResourceURLs: ["/healthz"]
  verbs: ["get"]
- apiGroups: ["apps"]
  resources: ["deployments"]
  resourceNames: ["controller", "worker"]
  verbs: ["get", "watch"]
- apiGroups: [""]
  resources: ["nodes", "deployments"]
  resourceNames: ["worker", "controller"]
  verbs: ["watch", "get"]
- nonResourceURLs: ["/metrics"]
  verbs: ["get"]
- apiGroups: ["apps"]
  resources: ["nodes"]
  resourceNames: ["worker", "controller"]
  verbs: ["watch", "get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: unrelated-role
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get"]
`)

	cmd := exec.Command(
		"go", "run", ".",
		"--generated", generated,
		"--generated-role", "manager-role",
		"--rendered", rendered,
		"--rendered-role", "chart-controller-role",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected equivalent permissions to pass, got %v:\n%s", err, output)
	}
}

func TestPermissionDriftReportsMissingAndExcessPermissions(t *testing.T) {
	t.Parallel()

	generated := writeManifest(t, "generated.yaml", `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: manager-role
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get"]
- apiGroups: ["apps"]
  resources: ["deployments"]
  resourceNames: ["controller"]
  verbs: ["update"]
- nonResourceURLs: ["/healthz"]
  verbs: ["get"]
`)
	rendered := writeManifest(t, "rendered.yaml", `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: chart-controller-role
rules:
- apiGroups: ["apps"]
  resources: ["deployments"]
  resourceNames: ["controller"]
  verbs: ["update"]
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["delete"]
- nonResourceURLs: ["/metrics"]
  verbs: ["get"]
`)

	cmd := exec.Command(
		"go", "run", ".",
		"--generated", generated,
		"--generated-role", "manager-role",
		"--rendered", rendered,
		"--rendered-role", "chart-controller-role",
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected permission drift to fail:\n%s", output)
	}

	for _, expected := range []string{
		"missing permissions:",
		`resource apiGroup="" resource="pods" resourceName="*" verb="get"`,
		`nonResourceURL="/healthz" verb="get"`,
		"excess permissions:",
		`resource apiGroup="" resource="secrets" resourceName="*" verb="delete"`,
		`nonResourceURL="/metrics" verb="get"`,
	} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func writeManifest(t *testing.T, name, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	return path
}
