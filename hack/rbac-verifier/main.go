package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type options struct {
	generatedPath string
	generatedRole string
	renderedPath  string
	renderedRole  string
}

type permission struct {
	apiGroup         string
	resource         string
	resourceName     string
	allResourceNames bool
	nonResourceURL   string
	verb             string
}

func main() {
	opts := options{}
	flag.StringVar(&opts.generatedPath, "generated", "", "path to controller-generated RBAC YAML")
	flag.StringVar(&opts.generatedRole, "generated-role", "", "name of the controller-generated ClusterRole")
	flag.StringVar(&opts.renderedPath, "rendered", "", "path to rendered Helm YAML")
	flag.StringVar(&opts.renderedRole, "rendered-role", "", "name of the rendered manager ClusterRole")
	flag.Parse()

	if err := verify(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verify(opts options) error {
	generated, err := loadClusterRole(opts.generatedPath, opts.generatedRole)
	if err != nil {
		return fmt.Errorf("loading generated RBAC: %w", err)
	}
	rendered, err := loadClusterRole(opts.renderedPath, opts.renderedRole)
	if err != nil {
		return fmt.Errorf("loading rendered RBAC: %w", err)
	}

	generatedPermissions := permissionSet(generated.Rules)
	renderedPermissions := permissionSet(rendered.Rules)
	missing := difference(generatedPermissions, renderedPermissions)
	excess := difference(renderedPermissions, generatedPermissions)
	if len(missing) > 0 || len(excess) > 0 {
		return permissionDriftError(missing, excess)
	}

	fmt.Println("controller RBAC permissions match the rendered manager role")
	return nil
}

func loadClusterRole(path, name string) (*rbacv1.ClusterRole, error) {
	if path == "" || name == "" {
		return nil, fmt.Errorf("manifest path and role name are required")
	}

	manifest, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	for {
		role := &rbacv1.ClusterRole{}
		if err := decoder.Decode(role); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if role.Kind == "ClusterRole" && role.Name == name {
			return role, nil
		}
	}

	return nil, fmt.Errorf("ClusterRole %q not found in %s", name, path)
}

func permissionSet(rules []rbacv1.PolicyRule) map[permission]struct{} {
	permissions := map[permission]struct{}{}
	for _, rule := range rules {
		resourceNames := rule.ResourceNames
		allResourceNames := len(resourceNames) == 0
		if allResourceNames {
			resourceNames = []string{""}
		}
		for _, apiGroup := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, resourceName := range resourceNames {
					for _, verb := range rule.Verbs {
						permissions[permission{
							apiGroup:         apiGroup,
							resource:         resource,
							resourceName:     resourceName,
							allResourceNames: allResourceNames,
							verb:             verb,
						}] = struct{}{}
					}
				}
			}
		}
		for _, nonResourceURL := range rule.NonResourceURLs {
			for _, verb := range rule.Verbs {
				permissions[permission{
					nonResourceURL: nonResourceURL,
					verb:           verb,
				}] = struct{}{}
			}
		}
	}
	return permissions
}

func difference(left, right map[permission]struct{}) []permission {
	diff := make([]permission, 0)
	for candidate := range left {
		if _, ok := right[candidate]; !ok {
			diff = append(diff, candidate)
		}
	}
	sort.Slice(diff, func(i, j int) bool {
		return diff[i].String() < diff[j].String()
	})
	return diff
}

func permissionDriftError(missing, excess []permission) error {
	var message strings.Builder
	message.WriteString("controller RBAC permissions do not match the rendered manager role")
	if len(missing) > 0 {
		message.WriteString("\nmissing permissions:")
		for _, item := range missing {
			fmt.Fprintf(&message, "\n  - %s", item.String())
		}
	}
	if len(excess) > 0 {
		message.WriteString("\nexcess permissions:")
		for _, item := range excess {
			fmt.Fprintf(&message, "\n  - %s", item.String())
		}
	}
	return errors.New(message.String())
}

func (item permission) String() string {
	if item.nonResourceURL != "" {
		return fmt.Sprintf("nonResourceURL=%q verb=%q", item.nonResourceURL, item.verb)
	}
	resourceName := fmt.Sprintf("resourceName=%q", item.resourceName)
	if item.allResourceNames {
		resourceName = "resourceNames=<all>"
	}
	return fmt.Sprintf(
		"resource apiGroup=%q resource=%q %s verb=%q",
		item.apiGroup,
		item.resource,
		resourceName,
		item.verb,
	)
}
