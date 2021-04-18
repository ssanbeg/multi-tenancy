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

package config

import (
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/record"

	vcclient "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/client/clientset/versioned"
	vcinformers "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/client/informers/externalversions/tenancy/v1alpha1"
	syncerconfig "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/apis/config"
)

// Config has all the context to run a Syncer.
type Config struct {
	// config is the syncer's configuration object.
	ComponentConfig syncerconfig.SyncerConfiguration

	// virtual cluster CR client
	VirtualClusterClient   vcclient.Interface
	VirtualClusterInformer vcinformers.VirtualClusterInformer

	// the meta cluster client
	MetaClusterClient clientset.Interface

	// the super cluster client
	SuperClusterClient          clientset.Interface
	SuperClusterInformerFactory informers.SharedInformerFactory

	// the client only used for leader election
	LeaderElectionClient clientset.Interface

	// the rest config for the super cluster
	Kubeconfig *restclient.Config

	// the event sink
	Recorder    record.EventRecorder
	Broadcaster record.EventBroadcaster

	// LeaderElection is optional.
	LeaderElection *leaderelection.LeaderElectionConfig

	// server config.
	Address  string
	Port     string
	CertFile string
	KeyFile  string
}

type completedConfig struct {
	*Config
}

// CompletedConfig same as Config, just to swap private object.
type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() *CompletedConfig {
	cc := completedConfig{c}
	return &CompletedConfig{&cc}
}
