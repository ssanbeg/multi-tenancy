/*
Copyright 2021 The Kubernetes Authors.

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

package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"

	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/constants"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/conversion"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/metrics"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/util"
)

var numSpecMissMatchedServices uint64
var numStatusMissMatchedServices uint64
var numUWMetaMissMatchedServices uint64

func (c *controller) StartPatrol(stopCh <-chan struct{}) error {
	if !cache.WaitForCacheSync(stopCh, c.serviceSynced) {
		return fmt.Errorf("failed to wait for caches to sync before starting Service checker")
	}
	c.Patroller.Start(stopCh)
	return nil
}

// PatrollerDo check if services keep consistency between super
// master and tenant masters.
func (c *controller) PatrollerDo() {
	clusterNames := c.MultiClusterController.GetClusterNames()
	if len(clusterNames) == 0 {
		klog.Infof("tenant masters has no clusters, give up period checker")
		return
	}

	wg := sync.WaitGroup{}
	numSpecMissMatchedServices = 0
	numStatusMissMatchedServices = 0
	numUWMetaMissMatchedServices = 0

	for _, clusterName := range clusterNames {
		wg.Add(1)
		go func(clusterName string) {
			defer wg.Done()
			c.checkServicesOfTenantCluster(clusterName)
		}(clusterName)
	}
	wg.Wait()

	pServices, err := c.serviceLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("error listing services from super master informer cache: %v", err)
		return
	}

	for _, pService := range pServices {
		clusterName, vNamespace := conversion.GetVirtualOwner(pService)
		if len(clusterName) == 0 || len(vNamespace) == 0 {
			continue
		}
		shouldDelete := false
		vServiceObj, err := c.MultiClusterController.Get(clusterName, vNamespace, pService.Name)
		if errors.IsNotFound(err) {
			shouldDelete = true
		}
		if err == nil {
			vService := vServiceObj.(*v1.Service)
			if pService.Annotations[constants.LabelUID] != string(vService.UID) {
				shouldDelete = true
				klog.Warningf("Found pService %s/%s delegated UID is different from tenant object.", pService.Namespace, pService.Name)
			}
		}
		if shouldDelete {
			deleteOptions := metav1.NewPreconditionDeleteOptions(string(pService.UID))
			if err = c.serviceClient.Services(pService.Namespace).Delete(context.TODO(), pService.Name, *deleteOptions); err != nil {
				klog.Errorf("error deleting pService %s/%s in super master: %v", pService.Namespace, pService.Name, err)
			} else {
				metrics.CheckerRemedyStats.WithLabelValues("DeletedOrphanSuperMasterServices").Inc()
			}
		}
	}

	metrics.CheckerMissMatchStats.WithLabelValues("SpecMissMatchedServices").Set(float64(numSpecMissMatchedServices))
	metrics.CheckerMissMatchStats.WithLabelValues("StatusMissMatchedServices").Set(float64(numStatusMissMatchedServices))
	metrics.CheckerMissMatchStats.WithLabelValues("UWMetaMissMatchedServices").Set(float64(numUWMetaMissMatchedServices))
}

func (c *controller) checkServicesOfTenantCluster(clusterName string) {
	listObj, err := c.MultiClusterController.List(clusterName)
	if err != nil {
		klog.Errorf("error listing services from cluster %s informer cache: %v", clusterName, err)
		return
	}
	klog.V(4).Infof("check services consistency in cluster %s", clusterName)
	svcList := listObj.(*v1.ServiceList)
	for i, vService := range svcList.Items {
		targetNamespace := conversion.ToSuperMasterNamespace(clusterName, vService.Namespace)
		pService, err := c.serviceLister.Services(targetNamespace).Get(vService.Name)
		if errors.IsNotFound(err) {
			if err := c.MultiClusterController.RequeueObject(clusterName, &svcList.Items[i]); err != nil {
				klog.Errorf("error requeue vservice %v/%v in cluster %s: %v", vService.Namespace, vService.Name, clusterName, err)
			} else {
				metrics.CheckerRemedyStats.WithLabelValues("RequeuedTenantServices").Inc()
			}
			continue
		}

		if err != nil {
			klog.Errorf("failed to get pService %s/%s from super master cache: %v", targetNamespace, vService.Name, err)
			continue
		}

		if pService.Annotations[constants.LabelUID] != string(vService.UID) {
			klog.Errorf("Found pService %s/%s delegated UID is different from tenant object.", targetNamespace, pService.Name)
			continue
		}

		vc, err := util.GetVirtualClusterObject(c.MultiClusterController, clusterName)
		if err != nil {
			klog.Errorf("fail to get cluster spec : %s", clusterName)
			continue
		}
		updatedService := conversion.Equality(c.Config, vc).CheckServiceEquality(pService, &svcList.Items[i])
		if updatedService != nil {
			atomic.AddUint64(&numSpecMissMatchedServices, 1)
			klog.Warningf("spec of service %v/%v diff in super&tenant master", vService.Namespace, vService.Name)
			if err := c.MultiClusterController.RequeueObject(clusterName, &svcList.Items[i]); err != nil {
				klog.Errorf("error requeue vservice %v/%v in cluster %s: %v", vService.Namespace, vService.Name, clusterName, err)
			} else {
				metrics.CheckerRemedyStats.WithLabelValues("RequeuedTenantServices").Inc()
			}
		}
		if isBackPopulateService(pService) {
			enqueue := false
			updatedMeta := conversion.Equality(c.Config, vc).CheckUWObjectMetaEquality(&pService.ObjectMeta, &svcList.Items[i].ObjectMeta)
			if updatedMeta != nil {
				atomic.AddUint64(&numUWMetaMissMatchedServices, 1)
				enqueue = true
				klog.Warningf("UWObjectMeta of vService %v/%v diff in super&tenant master", vService.Namespace, vService.Name)
			}
			if !equality.Semantic.DeepEqual(vService.Status, pService.Status) {
				enqueue = true
				atomic.AddUint64(&numStatusMissMatchedServices, 1)
				klog.Warningf("Status of vService %v/%v diff in super&tenant master", vService.Namespace, vService.Name)
			}
			if enqueue {
				c.enqueueService(pService)
			}
		}
	}
}
