/*
Copyright 2019 The Kubernetes Authors.

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

package storageclass

import (
	"fmt"

	v1 "k8s.io/api/storage/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	storageinformers "k8s.io/client-go/informers/storage/v1"
	clientset "k8s.io/client-go/kubernetes"
	v1storage "k8s.io/client-go/kubernetes/typed/storage/v1"
	listersv1 "k8s.io/client-go/listers/storage/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"

	vcclient "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/client/clientset/versioned"
	vcinformers "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/client/informers/externalversions/tenancy/v1alpha1"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/apis/config"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/constants"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/manager"
	pa "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/patrol"
	uw "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/uwcontroller"
	mc "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/util/mccontroller"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/util/plugin"
)

func init() {
	plugin.SyncerResourceRegister.Register(&plugin.Registration{
		ID: "storageclass",
		InitFn: func(ctx *plugin.InitContext) (interface{}, error) {
			return NewStorageClassController(ctx.Config.(*config.SyncerConfiguration), ctx.Client, ctx.Informer, ctx.VCClient, ctx.VCInformer, manager.ResourceSyncerOptions{})
		},
	})
}

type controller struct {
	manager.BaseResourceSyncer
	// super master storageclasses client
	client v1storage.StorageClassesGetter
	// super master storageclasses informer/lister/synced functions
	informer           storageinformers.Interface
	storageclassLister listersv1.StorageClassLister
	storageclassSynced cache.InformerSynced
}

func NewStorageClassController(config *config.SyncerConfiguration,
	client clientset.Interface,
	informer informers.SharedInformerFactory,
	vcClient vcclient.Interface,
	vcInformer vcinformers.VirtualClusterInformer,
	options manager.ResourceSyncerOptions) (manager.ResourceSyncer, error) {
	c := &controller{
		BaseResourceSyncer: manager.BaseResourceSyncer{
			Config: config,
		},
		client:   client.StorageV1(),
		informer: informer.Storage().V1(),
	}

	var err error
	c.MultiClusterController, err = mc.NewMCController(&v1.StorageClass{}, &v1.StorageClassList{}, c, mc.WithOptions(options.MCOptions))
	if err != nil {
		return nil, err
	}

	c.storageclassLister = informer.Storage().V1().StorageClasses().Lister()
	if options.IsFake {
		c.storageclassSynced = func() bool { return true }
	} else {
		c.storageclassSynced = informer.Storage().V1().StorageClasses().Informer().HasSynced
	}

	c.UpwardController, err = uw.NewUWController(&v1.StorageClass{}, c, uw.WithOptions(options.UWOptions))
	if err != nil {
		return nil, err
	}

	c.Patroller, err = pa.NewPatroller(&v1.StorageClass{}, c, pa.WithOptions(options.PatrolOptions))
	if err != nil {
		return nil, err
	}

	c.informer.StorageClasses().Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				switch t := obj.(type) {
				case *v1.StorageClass:
					return publicStorageClass(t)
				case cache.DeletedFinalStateUnknown:
					if e, ok := t.Obj.(*v1.StorageClass); ok {
						return publicStorageClass(e)
					}
					utilruntime.HandleError(fmt.Errorf("unable to convert object %v to *v1.StorageClass", obj))
					return false
				default:
					utilruntime.HandleError(fmt.Errorf("unable to handle object in super master storageclass controller: %v", obj))
					return false
				}
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: c.enqueueStorageClass,
				UpdateFunc: func(oldObj, newObj interface{}) {
					newStorageClass := newObj.(*v1.StorageClass)
					oldStorageClass := oldObj.(*v1.StorageClass)
					if newStorageClass.ResourceVersion != oldStorageClass.ResourceVersion {
						c.enqueueStorageClass(newObj)
					}
				},
				DeleteFunc: c.enqueueStorageClass,
			},
		})
	return c, nil
}

func publicStorageClass(e *v1.StorageClass) bool {
	// We only backpopulate specific storageclass to tenant masters
	return e.Labels[constants.PublicObjectKey] == "true"
}

func (c *controller) enqueueStorageClass(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %v: %v", obj, err))
		return
	}

	clusterNames := c.MultiClusterController.GetClusterNames()
	if len(clusterNames) == 0 {
		klog.Infof("No tenant masters, stop backpopulate storageclass %v", key)
		return
	}

	for _, clusterName := range clusterNames {
		c.UpwardController.AddToQueue(clusterName + "/" + key)
	}
}
