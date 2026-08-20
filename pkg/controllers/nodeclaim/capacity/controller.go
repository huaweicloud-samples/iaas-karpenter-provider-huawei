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
	"context"
	"fmt"

	"github.com/awslabs/operatorpkg/reasonable"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/instancetype"
)

// Controller feeds the discovered capacity cache from registered nodes.
type Controller struct {
	kubeClient           client.Client
	instanceTypeProvider *instancetype.DefaultProvider
}

func NewController(kubeClient client.Client, instanceTypeProvider *instancetype.DefaultProvider) *Controller {
	return &Controller{kubeClient: kubeClient, instanceTypeProvider: instanceTypeProvider}
}

func (c *Controller) Name() string {
	return "nodeclaim.capacity"
}

func (c *Controller) Reconcile(ctx context.Context, node *corev1.Node) (reconcile.Result, error) {
	if !isRegisteredKarpenterNode(node) {
		return reconcile.Result{}, nil
	}

	nodeClaim, err := c.nodeClaimForNode(ctx, node)
	if err != nil {
		return reconcile.Result{}, err
	}
	if nodeClaim == nil {
		return reconcile.Result{}, nil
	}

	nodeClass := &v1alpha1.CCENodeClass{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting nodeclass, %w", err)
	}

	if err := c.instanceTypeProvider.UpdateInstanceTypeCapacityFromNode(ctx, node, nodeClaim, nodeClass); err != nil {
		return reconcile.Result{}, fmt.Errorf("updating discovered capacity, %w", err)
	}
	return reconcile.Result{}, nil
}

func (c *Controller) nodeClaimForNode(ctx context.Context, node *corev1.Node) (*karpv1.NodeClaim, error) {
	nodeClaims := &karpv1.NodeClaimList{}
	if err := c.kubeClient.List(ctx, nodeClaims); err != nil {
		return nil, fmt.Errorf("listing nodeclaims, %w", err)
	}
	for i := range nodeClaims.Items {
		nc := &nodeClaims.Items[i]
		if nc.Status.NodeName == node.Name || (nc.Status.ProviderID != "" && nc.Status.ProviderID == node.Spec.ProviderID) {
			return nc, nil
		}
	}
	return nil, nil
}

func isRegisteredKarpenterNode(node *corev1.Node) bool {
	if node.Labels[karpv1.NodePoolLabelKey] == "" {
		return false
	}
	if node.Labels[karpv1.NodeRegisteredLabelKey] != "true" {
		return false
	}
	if node.Labels[corev1.LabelInstanceTypeStable] == "" {
		return false
	}
	return !node.Status.Capacity.Memory().IsZero()
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named(c.Name()).
		For(&corev1.Node{}, builder.WithPredicates(
			predicate.NewPredicateFuncs(func(o client.Object) bool {
				return isRegisteredKarpenterNode(o.(*corev1.Node))
			}),
			predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool { return true },
				UpdateFunc: func(e event.UpdateEvent) bool { return true },
				DeleteFunc: func(e event.DeleteEvent) bool { return false },
			},
		)).
		WithOptions(controller.Options{
			RateLimiter:             reasonable.RateLimiter(),
			MaxConcurrentReconciles: 10,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
