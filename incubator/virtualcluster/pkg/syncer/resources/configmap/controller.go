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

package configmap

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	vcclient "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/client/clientset/versioned"
	vcinformers "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/client/informers/externalversions/tenancy/v1alpha1"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/apis/config"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/manager"
	pa "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/patrol"
	mc "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/util/mccontroller"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/util/plugin"
)

func init() {
	plugin.SyncerResourceRegister.Register(&plugin.Registration{
		ID: "configmap",
		InitFn: func(ctx *plugin.InitContext) (interface{}, error) {
			return NewConfigMapController(ctx.Config.(*config.SyncerConfiguration), ctx.Client, ctx.Informer, ctx.VCClient, ctx.VCInformer, manager.ResourceSyncerOptions{})
		},
	})
}

type controller struct {
	manager.BaseResourceSyncer
	// super master configMap client
	configMapClient v1core.ConfigMapsGetter
	// super master configMap informer lister/synced function
	configMapLister listersv1.ConfigMapLister
	configMapSynced cache.InformerSynced
}

func NewConfigMapController(config *config.SyncerConfiguration,
	client clientset.Interface,
	informer informers.SharedInformerFactory,
	vcClient vcclient.Interface,
	vcInformer vcinformers.VirtualClusterInformer,
	options manager.ResourceSyncerOptions) (manager.ResourceSyncer, error) {

	c := &controller{
		BaseResourceSyncer: manager.BaseResourceSyncer{
			Config: config,
		},
		configMapClient: client.CoreV1(),
	}

	var err error
	c.MultiClusterController, err = mc.NewMCController(&v1.ConfigMap{}, &v1.ConfigMapList{}, c, mc.WithOptions(options.MCOptions))
	if err != nil {
		return nil, err
	}

	c.configMapLister = informer.Core().V1().ConfigMaps().Lister()
	if options.IsFake {
		c.configMapSynced = func() bool { return true }
	} else {
		c.configMapSynced = informer.Core().V1().ConfigMaps().Informer().HasSynced
	}

	c.Patroller, err = pa.NewPatroller(&v1.ConfigMap{}, c, pa.WithOptions(options.PatrolOptions))
	if err != nil {
		return nil, err
	}

	return c, nil
}
