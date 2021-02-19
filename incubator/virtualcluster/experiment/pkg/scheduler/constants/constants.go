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

package constants

import (
	"fmt"
	"math"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/version"
)

const (

	// Override the client-go default 5 qps and 10 burst
	DefaultSchedulerClientQPS   = 100
	DefaultSchedulerClientBurst = 500

	// DefaultRequestTimeout is set for all client-go request. This is the absolute
	// timeout of the HTTP request, including reading the response body.
	DefaultRequestTimeout = 30 * time.Second

	VirtualClusterWorker = 3
	SuperClusterWorker   = 3

	KubeconfigAdminSecretName = "admin-kubeconfig"

	InternalSchedulerCache   = "tenancy.x-k8s.io/schedulercache"
	InternalSchedulerEngine  = "tenancy.x-k8s.io/schedulerengine"
	InternalSchedulerManager = "tenancy.x-k8s.io/schedulermanager"
)

var SchedulerUserAgent = "scheduler" + version.BriefVersion()

// shadowcluster has a fake "unlimited" capacity
var ShadowClusterCapacity = v1.ResourceList{
	v1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", math.MaxInt32)),
	v1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dGi", math.MaxInt32)),
}
