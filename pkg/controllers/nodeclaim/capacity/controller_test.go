/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package capacity

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestIsRegisteredKarpenterNode(t *testing.T) {
	base := func() *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "n1",
				Labels: map[string]string{
					karpv1.NodePoolLabelKey:        "poc",
					karpv1.NodeRegisteredLabelKey:  "true",
					corev1.LabelInstanceTypeStable: "t7.large.2",
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("3762Mi"),
				},
			},
		}
	}

	if !isRegisteredKarpenterNode(base()) {
		t.Fatal("expected registered karpenter node to match")
	}

	n := base()
	delete(n.Labels, karpv1.NodePoolLabelKey)
	if isRegisteredKarpenterNode(n) {
		t.Fatal("node without nodepool label must not match")
	}

	n = base()
	n.Labels[karpv1.NodeRegisteredLabelKey] = "false"
	if isRegisteredKarpenterNode(n) {
		t.Fatal("unregistered node must not match")
	}

	n = base()
	n.Status.Capacity = corev1.ResourceList{}
	if isRegisteredKarpenterNode(n) {
		t.Fatal("node without memory capacity must not match")
	}
}
