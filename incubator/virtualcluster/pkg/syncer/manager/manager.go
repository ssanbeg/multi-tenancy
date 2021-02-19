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

package manager

import (
	"sync"

	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"

	vcclient "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/client/clientset/versioned"
	vcinformers "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/client/informers/externalversions/tenancy/v1alpha1"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/apis/config"
	pa "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/patrol"
	uw "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/uwcontroller"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/util/listener"
	mc "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/util/mccontroller"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/util/reconciler"
)

// ControllerManager manages number of resource syncers. It starts their caches, waits for those to sync,
// then starts the controllers.
type ControllerManager struct {
	resourceSyncers map[ResourceSyncer]struct{}
}

type ResourceSyncerOptions struct {
	MCOptions     *mc.Options
	UWOptions     *uw.Options
	PatrolOptions *pa.Options
	IsFake        bool
}

func New() *ControllerManager {
	return &ControllerManager{resourceSyncers: make(map[ResourceSyncer]struct{})}
}

// ResourceSyncer is the interface used by ControllerManager to manage multiple resource syncers.
type ResourceSyncer interface {
	reconciler.DWReconciler
	reconciler.UWReconciler
	reconciler.PatrolReconciler
	GetMCController() *mc.MultiClusterController
	GetUpwardController() *uw.UpwardController
	GetListener() listener.ClusterChangeListener
	StartUWS(stopCh <-chan struct{}) error
	StartDWS(stopCh <-chan struct{}) error
	StartPatrol(stopCh <-chan struct{}) error
}

// AddController adds a resource syncer to the ControllerManager.
func (m *ControllerManager) AddResourceSyncer(s ResourceSyncer) {
	m.resourceSyncers[s] = struct{}{}

	l := s.GetListener()
	if l == nil {
		panic("resource Syncer should provide listener")
	}

	listener.AddListener(l)
}

type ResourceSyncerNew func(*config.SyncerConfiguration,
	clientset.Interface,
	informers.SharedInformerFactory,
	vcclient.Interface,
	vcinformers.VirtualClusterInformer, ResourceSyncerOptions) (ResourceSyncer, error)

type BaseResourceSyncer struct {
	Config                 *config.SyncerConfiguration
	MultiClusterController *mc.MultiClusterController
	UpwardController       *uw.UpwardController
	Patroller              *pa.Patroller
}

var _ ResourceSyncer = &BaseResourceSyncer{}

func (b *BaseResourceSyncer) Reconcile(request reconciler.Request) (reconciler.Result, error) {
	return reconciler.Result{}, nil
}

func (b *BaseResourceSyncer) BackPopulate(s string) error {
	return nil
}

func (b *BaseResourceSyncer) PatrollerDo() {
	return
}

func (b *BaseResourceSyncer) GetListener() listener.ClusterChangeListener {
	return listener.NewMCControllerListener(b.MultiClusterController)
}

func (b *BaseResourceSyncer) GetMCController() *mc.MultiClusterController {
	return b.MultiClusterController
}

func (b *BaseResourceSyncer) GetUpwardController() *uw.UpwardController {
	return b.UpwardController
}

func (b *BaseResourceSyncer) StartUWS(stopCh <-chan struct{}) error {
	return nil
}

func (b *BaseResourceSyncer) StartDWS(stopCh <-chan struct{}) error {
	return nil
}

func (b *BaseResourceSyncer) StartPatrol(stopCh <-chan struct{}) error {
	return nil
}

// Start gets all the unique caches of the controllers it manages, starts them,
// then starts the controllers as soon as their respective caches are synced.
// Start blocks until an error or stop is received.
func (m *ControllerManager) Start(stop <-chan struct{}) error {
	errCh := make(chan error)

	wg := &sync.WaitGroup{}
	wg.Add(len(m.resourceSyncers) * 3)

	for s := range m.resourceSyncers {
		go func(s ResourceSyncer) {
			defer wg.Done()
			if err := s.StartDWS(stop); err != nil {
				errCh <- err
			}
		}(s)
		// start UWS syncer
		go func(s ResourceSyncer) {
			defer wg.Done()
			if err := s.StartUWS(stop); err != nil {
				errCh <- err
			}
		}(s)
		// start periodic checker
		go func(s ResourceSyncer) {
			defer wg.Done()
			if err := s.StartPatrol(stop); err != nil {
				errCh <- err
			}
		}(s)
	}

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
		return nil
	case <-stop:
		return nil
	case err := <-errCh:
		return err
	}
}
