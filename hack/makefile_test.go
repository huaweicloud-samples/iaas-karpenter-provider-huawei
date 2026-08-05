package hack_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2ETargetCleansKindClusterAfterFailure(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	repoRoot := filepath.Dir(wd)

	testCases := []struct {
		name          string
		environment   string
		expectedError string
	}{
		{name: "e2e tests fail", environment: "FAIL_GO_TEST=true", expectedError: "Error 23"},
		{name: "manifest generation fails", environment: "FAIL_CONTROLLER_GEN=true", expectedError: "Error 24"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			binDir := t.TempDir()
			kindLog := filepath.Join(t.TempDir(), "kind.log")
			controllerGen := filepath.Join(binDir, "controller-gen")

			writeExecutable(t, filepath.Join(binDir, "go"), `#!/bin/sh
case "$1 $2" in
  "env GOBIN") exit 0 ;;
  "env GOPATH") echo /tmp ;;
  "env GOVERSION") echo go1.26.0 ;;
esac
if [ "$1" = "test" ] && [ "${FAIL_GO_TEST:-}" = "true" ]; then
  exit 23
fi
exit 0
`)
			writeExecutable(t, filepath.Join(binDir, "kind"), `#!/bin/sh
if [ "$1 $2" = "get clusters" ]; then
  exit 0
elif [ "$1 $2" = "create cluster" ] || [ "$1 $2" = "delete cluster" ]; then
  printf '%s\n' "$*" >>"$KIND_LOG"
fi
`)
			writeExecutable(t, filepath.Join(binDir, "helm"), "#!/bin/sh\nexit 0\n")
			writeExecutable(t, controllerGen, `#!/bin/sh
if [ "${FAIL_CONTROLLER_GEN:-}" = "true" ] && [ "$1" = "crd" ]; then
  exit 24
fi
exit 0
`)

			cmd := exec.Command(
				"make",
				"-o", controllerGen,
				"test-e2e",
				"CONTROLLER_GEN="+controllerGen,
				"KIND="+filepath.Join(binDir, "kind"),
				"HELM="+filepath.Join(binDir, "helm"),
			)
			cmd.Dir = repoRoot
			cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "KIND_LOG="+kindLog, testCase.environment)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected make test-e2e to preserve the failure, output:\n%s", output)
			}
			if !strings.Contains(string(output), testCase.expectedError) {
				t.Fatalf("expected original failure %q, output:\n%s", testCase.expectedError, output)
			}

			cleanupLog, err := os.ReadFile(kindLog)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("reading kind log: %v", err)
			}
			if !strings.Contains(string(cleanupLog), "create cluster --name karpenter-provider-huawei-test-e2e") {
				t.Fatalf("expected Kind cluster creation before failure, kind log:\n%s\nmake output:\n%s", cleanupLog, output)
			}
			if !strings.Contains(string(cleanupLog), "delete cluster --name karpenter-provider-huawei-test-e2e") {
				t.Fatalf("expected Kind cluster cleanup after failure, kind log:\n%s\nmake output:\n%s", cleanupLog, output)
			}
		})
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("writing executable %s: %v", path, err)
	}
}
