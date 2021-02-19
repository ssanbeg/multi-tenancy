package blockuseofbindmounts

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/multi-tenancy/benchmarks/kubectl-mtb/bundle/box"
	"sigs.k8s.io/multi-tenancy/benchmarks/kubectl-mtb/pkg/benchmark"
	"sigs.k8s.io/multi-tenancy/benchmarks/kubectl-mtb/test"
	"sigs.k8s.io/multi-tenancy/benchmarks/kubectl-mtb/test/utils"
	podutil "sigs.k8s.io/multi-tenancy/benchmarks/kubectl-mtb/test/utils/resources/pod"
	"sigs.k8s.io/multi-tenancy/benchmarks/kubectl-mtb/types"
)

var b = &benchmark.Benchmark{

	PreRun: func(options types.RunOptions) error {

		resource := utils.GroupResource{
			APIGroup: "",
			APIResource: metav1.APIResource{
				Name: "pods",
			},
		}

		access, msg, err := utils.RunAccessCheck(options.Tenant1Client, options.TenantNamespace, resource, "create")
		if err != nil {
			options.Logger.Debug(err.Error())
			return err
		}
		if !access {
			return fmt.Errorf(msg)
		}

		return nil
	},

	Run: func(options types.RunOptions) error {

		// Host path
		inlineVolumeSources := []*v1.VolumeSource{
			{
				HostPath: &v1.HostPathVolumeSource{
					Path: "/tmp/busybox",
				},
			},
		}

		podSpec := &podutil.PodSpec{NS: options.TenantNamespace, InlineVolumeSources: inlineVolumeSources, RunAsNonRoot: true}
		err := podSpec.SetDefaults()
		if err != nil {
			options.Logger.Debug("Failed to create pod: ", err.Error())
			return err
		}

		// Try to create a pod using tenant-admin impersonation
		pod := podSpec.MakeSecPod()
		_, err = options.Tenant1Client.CoreV1().Pods(options.TenantNamespace).Create(context.TODO(), pod, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if err == nil {
			return fmt.Errorf("Tenant must not be allowed to create a pod with host path volumes")
		}
		options.Logger.Debug("Test passed")
		return nil
	},
}

func init() {
	// Get the []byte representation of a file, or an error if it doesn't exist:
	err := b.ReadConfig(box.Get("block_use_of_host_path/config.yaml"))
	if err != nil {
		fmt.Println(err.Error())
	}

	test.BenchmarkSuite.Add(b)
}
