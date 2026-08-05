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

	output, err := runVerifier(
		t,
		"--generated", generated,
		"--generated-role", "manager-role",
		"--rendered", rendered,
		"--rendered-role", "chart-controller-role",
	)
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

	output, err := runVerifier(
		t,
		"--generated", generated,
		"--generated-role", "manager-role",
		"--rendered", rendered,
		"--rendered-role", "chart-controller-role",
	)
	if err == nil {
		t.Fatalf("expected permission drift to fail:\n%s", output)
	}

	for _, expected := range []string{
		"missing permissions:",
		`resource apiGroup="" resource="pods" resourceNames=<all> verb="get"`,
		`nonResourceURL="/healthz" verb="get"`,
		"excess permissions:",
		`resource apiGroup="" resource="secrets" resourceNames=<all> verb="delete"`,
		`nonResourceURL="/metrics" verb="get"`,
	} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestUnrestrictedResourceNamesDifferFromLiteralAsterisk(t *testing.T) {
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
`)
	rendered := writeManifest(t, "rendered.yaml", `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: chart-controller-role
rules:
- apiGroups: [""]
  resources: ["pods"]
  resourceNames: ["*"]
  verbs: ["get"]
`)

	output, err := runVerifier(
		t,
		"--generated", generated,
		"--generated-role", "manager-role",
		"--rendered", rendered,
		"--rendered-role", "chart-controller-role",
	)
	if err == nil {
		t.Fatalf("expected unrestricted and literal resource names to differ:\n%s", output)
	}

	for _, expected := range []string{
		`resource apiGroup="" resource="pods" resourceNames=<all> verb="get"`,
		`resource apiGroup="" resource="pods" resourceName="*" verb="get"`,
	} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestInputErrorsAreActionable(t *testing.T) {
	t.Parallel()

	rendered := writeManifest(t, "rendered.yaml", `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: chart-controller-role
rules: []
`)
	missingPath := filepath.Join(t.TempDir(), "missing.yaml")
	malformed := writeManifest(t, "malformed.yaml", `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: manager-role
rules: [
`)
	wrongRole := writeManifest(t, "wrong-role.yaml", `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: another-role
rules: []
`)

	testCases := []struct {
		name     string
		args     []string
		expected []string
	}{
		{
			name:     "missing arguments",
			expected: []string{"loading generated RBAC: manifest path and role name are required"},
		},
		{
			name: "unreadable manifest",
			args: []string{
				"--generated", missingPath,
				"--generated-role", "manager-role",
				"--rendered", rendered,
				"--rendered-role", "chart-controller-role",
			},
			expected: []string{"loading generated RBAC:", "missing.yaml"},
		},
		{
			name: "malformed YAML",
			args: []string{
				"--generated", malformed,
				"--generated-role", "manager-role",
				"--rendered", rendered,
				"--rendered-role", "chart-controller-role",
			},
			expected: []string{"loading generated RBAC:", "did not find expected node content"},
		},
		{
			name: "role not found",
			args: []string{
				"--generated", wrongRole,
				"--generated-role", "manager-role",
				"--rendered", rendered,
				"--rendered-role", "chart-controller-role",
			},
			expected: []string{"loading generated RBAC:", `ClusterRole "manager-role" not found`},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			output, err := runVerifier(t, testCase.args...)
			if err == nil {
				t.Fatalf("expected invalid input to fail:\n%s", output)
			}
			for _, expected := range testCase.expected {
				if !strings.Contains(string(output), expected) {
					t.Errorf("expected output to contain %q, got:\n%s", expected, output)
				}
			}
		})
	}
}

func runVerifier(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()

	cmdArgs := append([]string{"run", "."}, args...)
	return exec.Command("go", cmdArgs...).CombinedOutput()
}

func writeManifest(t *testing.T, name, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	return path
}
