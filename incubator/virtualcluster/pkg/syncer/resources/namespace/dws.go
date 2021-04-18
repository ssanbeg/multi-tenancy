/*
Copyright 2020 The Kubernetes Authors.

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

package namespace

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"

	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/constants"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/conversion"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/util"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/util/featuregate"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/util/reconciler"
)

func (c *controller) StartDWS(stopCh <-chan struct{}) error {
	if !cache.WaitForCacheSync(stopCh, c.nsSynced) {
		return fmt.Errorf("failed to wait for caches to sync")
	}
	return c.MultiClusterController.Start(stopCh)
}

// The reconcile logic for tenant master namespace informer
func (c *controller) Reconcile(request reconciler.Request) (reconciler.Result, error) {
	klog.V(4).Infof("reconcile namespace %s for cluster %s", request.Name, request.ClusterName)
	targetNamespace := conversion.ToSuperMasterNamespace(request.ClusterName, request.Name)
	pNamespace, err := c.nsLister.Get(targetNamespace)
	pExists := true
	if err != nil {
		if !errors.IsNotFound(err) {
			return reconciler.Result{Requeue: true}, err
		}
		pExists = false
	}
	vExists := true
	vNamespaceObj, err := c.MultiClusterController.Get(request.ClusterName, request.Namespace, request.Name)
	if err != nil {
		if !errors.IsNotFound(err) {
			return reconciler.Result{Requeue: true}, err
		}
		vExists = false
	}

	if vExists && !pExists {
		vNamespace := vNamespaceObj.(*v1.Namespace)
		err := c.reconcileNamespaceCreate(request.ClusterName, targetNamespace, request.UID, vNamespace)
		if err != nil {
			klog.Errorf("failed reconcile namespace %s CREATE of cluster %s %v", request.Name, request.ClusterName, err)
			return reconciler.Result{Requeue: true}, err
		}
	} else if !vExists && pExists {
		err := c.reconcileNamespaceRemove(request.ClusterName, targetNamespace, request.UID, pNamespace)
		if err != nil {
			klog.Errorf("failed reconcile namespace %s DELETE of cluster %s %v", request.Name, request.ClusterName, err)
			return reconciler.Result{Requeue: true}, err
		}
	} else if vExists && pExists {
		vNamespace := vNamespaceObj.(*v1.Namespace)
		err := c.reconcileNamespaceUpdate(request.ClusterName, targetNamespace, request.UID, pNamespace, vNamespace)
		if err != nil {
			klog.Errorf("failed reconcile namespace %s UPDATE of cluster %s %v", request.Name, request.ClusterName, err)
			return reconciler.Result{Requeue: true}, err
		}
	} else {
		// object is gone.
	}
	return reconciler.Result{}, nil
}

func (c *controller) reconcileNamespaceCreate(clusterName, targetNamespace, requestUID string, vNamespace *v1.Namespace) error {
	vcName, vcNamespace, vcUID, err := c.MultiClusterController.GetOwnerInfo(clusterName)
	if err != nil {
		return err
	}

	newObj, err := conversion.BuildSuperMasterNamespace(clusterName, vcName, vcNamespace, vcUID, vNamespace)
	if err != nil {
		return err
	}

	_, err = c.namespaceClient.Namespaces().Create(context.TODO(), newObj.(*v1.Namespace), metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		klog.Infof("namespace %s of cluster %s already exist in super master", targetNamespace, clusterName)
		return nil
	}
	return err
}

func (c *controller) reconcileNamespaceUpdate(clusterName, targetNamespace, requestUID string, pNamespace, vNamespace *v1.Namespace) error {
	if pNamespace.Annotations[constants.LabelUID] != requestUID {
		return fmt.Errorf("pNamespace %s exists but its delegated UID is different", targetNamespace)
	}

	// update namespace meta is a generic operation, guarded by SuperClusterPooling for now
	if featuregate.DefaultFeatureGate.Enabled(featuregate.SuperClusterPooling) {
		vc, err := util.GetVirtualClusterObject(c.MultiClusterController, clusterName)
		if err != nil {
			return err
		}
		updatedNamespace := conversion.Equality(c.Config, vc).CheckNamespaceEquality(pNamespace, vNamespace)
		if updatedNamespace != nil {
			_, err = c.namespaceClient.Namespaces().Update(context.TODO(), updatedNamespace, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *controller) reconcileNamespaceRemove(clusterName, targetNamespace, requestUID string, pNamespace *v1.Namespace) error {
	if pNamespace.Annotations[constants.LabelUID] != requestUID {
		return fmt.Errorf("to be deleted pNamespace %s delegated UID is different from deleted object", targetNamespace)
	}

	opts := &metav1.DeleteOptions{
		PropagationPolicy: &constants.DefaultDeletionPolicy,
		Preconditions:     metav1.NewUIDPreconditions(string(pNamespace.UID)),
	}
	err := c.namespaceClient.Namespaces().Delete(context.TODO(), targetNamespace, *opts)
	if errors.IsNotFound(err) {
		klog.Warningf("namespace %s of cluster %s not found in super master", targetNamespace, clusterName)
		return nil
	}
	return err
}
