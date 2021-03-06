// Copyright 2019 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package helmreconciler

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"istio.io/istio/operator/pkg/object"
	"istio.io/istio/operator/pkg/util"
)

const (
	// MetadataNamespace is the namespace for mesh metadata (labels, annotations)
	MetadataNamespace = "install.operator.istio.io"

	// OwnerNameKey represents the name of the owner to which the resource relates
	OwnerNameKey = MetadataNamespace + "/owner-name"
)

var (
	// ordered by which types should be deleted, first to last
	namespacedResources = []schema.GroupVersionKind{
		{Group: "autoscaling", Version: "v2beta1", Kind: "HorizontalPodAutoscaler"},
		{Group: "policy", Version: "v1beta1", Kind: "PodDisruptionBudget"},
		{Group: "apps", Version: "v1", Kind: "StatefulSet"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "apps", Version: "v1", Kind: "DaemonSet"},
		{Group: "extensions", Version: "v1beta1", Kind: "Ingress"},
		{Group: "", Version: "v1", Kind: "Service"},
		// Endpoints are dynamically created, never from charts.
		// {Group: "", Version: "v1", Kind: "Endpoints"},
		{Group: "", Version: "v1", Kind: "ConfigMap"},
		{Group: "", Version: "v1", Kind: "PersistentVolumeClaim"},
		{Group: "", Version: "v1", Kind: "Pod"},
		{Group: "", Version: "v1", Kind: "Secret"},
		{Group: "", Version: "v1", Kind: "ServiceAccount"},
		{Group: "rbac.authorization.k8s.io", Version: "v1beta1", Kind: "RoleBinding"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"},
		{Group: "rbac.authorization.k8s.io", Version: "v1beta1", Kind: "Role"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "adapter"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "attributemanifest"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "handler"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "instance"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "HTTPAPISpec"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "HTTPAPISpecBinding"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "QuotaSpec"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "QuotaSpecBinding"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "rule"},
		{Group: "config.istio.io", Version: "v1alpha2", Kind: "template"},
		{Group: "networking.istio.io", Version: "v1alpha3", Kind: "DestinationRule"},
		{Group: "networking.istio.io", Version: "v1alpha3", Kind: "EnvoyFilter"},
		{Group: "networking.istio.io", Version: "v1alpha3", Kind: "Gateway"},
		{Group: "networking.istio.io", Version: "v1alpha3", Kind: "ServiceEntry"},
		{Group: "networking.istio.io", Version: "v1alpha3", Kind: "Sidecar"},
		{Group: "networking.istio.io", Version: "v1alpha3", Kind: "VirtualService"},
		{Group: "rbac.istio.io", Version: "v1alpha1", Kind: "ClusterRbacConfig"},
		{Group: "rbac.istio.io", Version: "v1alpha1", Kind: "RbacConfig"},
		{Group: "rbac.istio.io", Version: "v1alpha1", Kind: "ServiceRole"},
		{Group: "rbac.istio.io", Version: "v1alpha1", Kind: "ServiceRoleBinding"},
		{Group: "security.istio.io", Version: "v1beta1", Kind: "AuthorizationPolicy"},
		{Group: "security.istio.io", Version: "v1beta1", Kind: "RequestAuthentication"},
		{Group: "security.istio.io", Version: "v1beta1", Kind: "PeerAuthentication"},
	}

	// ordered by which types should be deleted, first to last
	nonNamespacedResources = []schema.GroupVersionKind{
		{Group: "admissionregistration.k8s.io", Version: "v1beta1", Kind: "MutatingWebhookConfiguration"},
		{Group: "admissionregistration.k8s.io", Version: "v1beta1", Kind: "ValidatingWebhookConfiguration"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
		// Cannot currently prune CRDs because this will also wipe out user config.
		// {Group: "apiextensions.k8s.io", Version: "v1beta1", Kind: "CustomResourceDefinition"},
	}
)

// Prune removes any resources not specified in manifests generated by HelmReconciler h.
func (h *HelmReconciler) Prune(manifests ChartManifestsMap) error {
	var errs util.Errors
	for cname, manifest := range manifests.Consolidated() {
		errs = util.AppendErr(errs, h.PruneUnlistedResources(allObjectHashes(manifest), cname))
	}
	return errs.ToError()
}

// Delete removes all resources associated with componentName.
func (h *HelmReconciler) DeleteComponent(componentName string) error {
	return h.PruneUnlistedResources(map[string]bool{}, componentName)
}

func (h *HelmReconciler) PruneUnlistedResources(excluded map[string]bool, componentName string) error {
	var errs util.Errors
	for _, gvk := range append(namespacedResources, nonNamespacedResources...) {
		objects := &unstructured.UnstructuredList{}
		objects.SetGroupVersionKind(gvk)
		labels, err := h.getOwnerLabels(componentName)
		if err != nil {
			return err
		}
		if err := h.client.List(context.TODO(), objects, client.MatchingLabels(labels), client.InNamespace(h.iop.Namespace)); err != nil {
			// we only want to retrieve resources clusters
			scope.Warnf("retrieving resources to prune type %s: %s not found", gvk.String(), err)
			continue
		}
		for _, o := range objects.Items {
			oh := object.NewK8sObject(&o, nil, nil).Hash()
			if excluded[oh] {
				continue
			}
			if h.opts.DryRun {
				h.opts.Log.LogAndPrintf("Not pruning object %s because of dry run.", oh)
				continue
			}

			err = h.client.Delete(context.TODO(), &o, client.PropagationPolicy(metav1.DeletePropagationBackground))
			if err != nil {
				errs = util.AppendErr(errs, err)
			}
			h.removeFromObjectCache(componentName, oh)
			h.opts.Log.LogAndPrintf("Pruned object %s.", oh)
		}
	}
	return errs.ToError()
}
