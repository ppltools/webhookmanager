/*
Copyright 2021 The Stupig Authors.
Copyright 2021 The Kruise Authors.

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

package deletionprotection

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ValidateNamespaceDeletion(c client.Client, namespace *v1.Namespace) error {
	pods := v1.PodList{}
	if err := c.List(context.TODO(), &pods, client.InNamespace(namespace.Name)); err != nil {
		return fmt.Errorf("forbidden by ResourcesProtectionDeletion for list pods error: %v", err)
	}
	var activeCount int
	for i := range pods.Items {
		pod := &pods.Items[i]
		if IsPodActive(pod) {
			activeCount++
		}
	}
	if activeCount > 0 {
		return fmt.Errorf("forbidden by ResourcesProtectionDeletion for %s=%s and active pods %d>0", activeCount)
	}
	return nil
}

func ValidateCRDDeletion(c client.Client, obj metav1.Object, gvk schema.GroupVersionKind) error {
	objList := &unstructured.UnstructuredList{}
	objList.SetAPIVersion(gvk.GroupVersion().String())
	objList.SetKind(gvk.Kind)
	if err := c.List(context.TODO(), objList, client.InNamespace(v1.NamespaceAll)); err != nil {
		return fmt.Errorf("failed to list CRs of %v: %v", gvk, err)
	}

	var activeCount int
	for i := range objList.Items {
		if objList.Items[i].GetDeletionTimestamp() == nil {
			activeCount++
		}
	}
	if activeCount > 0 {
		return fmt.Errorf("forbidden by ResourcesProtectionDeletion for %s=%s and active CRs %d>0", activeCount)
	}
	return nil
}

func IsPodActive(p *v1.Pod) bool {
	return v1.PodSucceeded != p.Status.Phase &&
		v1.PodFailed != p.Status.Phase &&
		p.DeletionTimestamp == nil
}
