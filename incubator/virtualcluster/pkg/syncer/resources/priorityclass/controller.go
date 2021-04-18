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

package priorityclass

import (
	"fmt"
	v1 "k8s.io/api/scheduling/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	priorityclassinformers "k8s.io/client-go/informers/scheduling/v1"
	clientset "k8s.io/client-go/kubernetes"
	v1priorityclass "k8s.io/client-go/kubernetes/typed/scheduling/v1"
	listersv1 "k8s.io/client-go/listers/scheduling/v1"
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
		ID: "priorityclass",
		InitFn: func(ctx *plugin.InitContext) (interface{}, error) {
			return NewPriorityClassController(ctx.Config.(*config.SyncerConfiguration), ctx.Client, ctx.Informer, ctx.VCClient, ctx.VCInformer, manager.ResourceSyncerOptions{})
		},
		Disable: true,
	})
}

type controller struct {
	manager.BaseResourceSyncer
	// super master priorityclasses client
	client v1priorityclass.PriorityClassesGetter
	// super master priorityclasses informer/lister/synced functions
	informer            priorityclassinformers.Interface
	priorityclassLister listersv1.PriorityClassLister
	priorityclassSynced cache.InformerSynced
}

func NewPriorityClassController(config *config.SyncerConfiguration,
	client clientset.Interface,
	informer informers.SharedInformerFactory,
	vcClient vcclient.Interface,
	vcInformer vcinformers.VirtualClusterInformer,
	options manager.ResourceSyncerOptions) (manager.ResourceSyncer, error) {
	c := &controller{
		BaseResourceSyncer: manager.BaseResourceSyncer{
			Config: config,
		},
		client:   client.SchedulingV1(),
		informer: informer.Scheduling().V1(),
	}

	var err error
	c.MultiClusterController, err = mc.NewMCController(&v1.PriorityClass{}, &v1.PriorityClassList{}, c, mc.WithOptions(options.MCOptions))
	if err != nil {
		return nil, err
	}

	c.priorityclassLister = informer.Scheduling().V1().PriorityClasses().Lister()
	if options.IsFake {
		c.priorityclassSynced = func() bool { return true }
	} else {
		c.priorityclassSynced = informer.Scheduling().V1().PriorityClasses().Informer().HasSynced
	}

	c.UpwardController, err = uw.NewUWController(&v1.PriorityClass{}, c, uw.WithOptions(options.UWOptions))
	if err != nil {
		return nil, err
	}

	c.Patroller, err = pa.NewPatroller(&v1.PriorityClass{}, c, pa.WithOptions(options.PatrolOptions))
	if err != nil {
		return nil, err
	}

	c.informer.PriorityClasses().Informer().AddEventHandler(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				switch t := obj.(type) {
				case *v1.PriorityClass:
					return publicPriorityClass(t)
				case cache.DeletedFinalStateUnknown:
					if e, ok := t.Obj.(*v1.PriorityClass); ok {
						return publicPriorityClass(e)
					}
					utilruntime.HandleError(fmt.Errorf("unable to convert object %v to *v1.PriorityClass", obj))
					return false
				default:
					utilruntime.HandleError(fmt.Errorf("unable to handle object in super master priorityclass controller: %v", obj))
					return false
				}
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: c.enqueuePriorityClass,
				UpdateFunc: func(oldObj, newObj interface{}) {
					newPriorityClass := newObj.(*v1.PriorityClass)
					oldPriorityClass := oldObj.(*v1.PriorityClass)
					if newPriorityClass.ResourceVersion != oldPriorityClass.ResourceVersion {
						c.enqueuePriorityClass(newObj)
					}
				},
				DeleteFunc: c.enqueuePriorityClass,
			},
		})
	return c, nil
}

func publicPriorityClass(e *v1.PriorityClass) bool {
	// We only backpopulate specific priorityclass to tenant masters
	return e.Labels[constants.PublicObjectKey] == "true"
}

func (c *controller) enqueuePriorityClass(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %v: %v", obj, err))
		return
	}

	clusterNames := c.MultiClusterController.GetClusterNames()
	if len(clusterNames) == 0 {
		klog.Infof("No tenant masters, stop backpopulate priorityclass %v", key)
		return
	}

	for _, clusterName := range clusterNames {
		c.UpwardController.AddToQueue(clusterName + "/" + key)
	}
}
