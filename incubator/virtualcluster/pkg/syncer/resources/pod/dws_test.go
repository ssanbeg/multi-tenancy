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

package pod

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	core "k8s.io/client-go/testing"
	"k8s.io/utils/pointer"

	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/apis/tenancy/v1alpha1"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/constants"
	"sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/conversion"
	util "sigs.k8s.io/multi-tenancy/incubator/virtualcluster/pkg/syncer/util/test"
)

const testTenantServiceAccountTokenSecretName = "default-token-jbrn5"
const testSuperServiceAccountTokenSecretName = "default-token-12345"

func tenantPod(name, namespace, uid string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Spec: v1.PodSpec{
			ServiceAccountName: "default",
			Containers: []v1.Container{
				{
					Image: "busybox",
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      testTenantServiceAccountTokenSecretName,
							MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
						},
						{
							Name:      "i-want-to-mount",
							MountPath: "/path",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: testTenantServiceAccountTokenSecretName,
					VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{
							SecretName: testTenantServiceAccountTokenSecretName,
						},
					},
				},
				{
					Name: "i-want-to-mount",
					VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{
							SecretName: testTenantServiceAccountTokenSecretName,
						},
					},
				},
			},
		},
	}
}

func tenantSecret(name, namespace, uid string) *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Type: v1.SecretTypeServiceAccountToken,
	}
}

func applyNodeNameToPod(vPod *v1.Pod, nodeName string) *v1.Pod {
	vPod.Spec.NodeName = nodeName
	return vPod
}

func applyDeletionTimestampToPod(vPod *v1.Pod, t time.Time, gracePeriodSeconds int64) *v1.Pod {
	metaTime := metav1.NewTime(t)
	vPod.DeletionTimestamp = &metaTime
	vPod.DeletionGracePeriodSeconds = pointer.Int64Ptr(gracePeriodSeconds)
	return vPod
}

func superPod(clusterKey, vcName, vcNamespace, name, namespace, uid string) *v1.Pod {
	return &v1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: conversion.ToSuperMasterNamespace(clusterKey, namespace),
			Labels: map[string]string{
				constants.LabelCluster:     clusterKey,
				constants.LabelVCName:      vcName,
				constants.LabelVCNamespace: vcNamespace,
			},
			Annotations: map[string]string{
				constants.LabelCluster:         clusterKey,
				constants.LabelNamespace:       namespace,
				constants.LabelOwnerReferences: "null",
				constants.LabelUID:             uid,
				constants.LabelVCName:          vcName,
				constants.LabelVCNamespace:     vcNamespace,
			},
		},
		Spec: v1.PodSpec{
			ServiceAccountName:           "default",
			AutomountServiceAccountToken: pointer.BoolPtr(false),
			Containers: []v1.Container{
				{
					Image: "busybox",
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      testSuperServiceAccountTokenSecretName,
							MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
						},
						{
							Name:      "i-want-to-mount",
							MountPath: "/path",
						},
					},
					Env: []v1.EnvVar{
						{
							Name:  "KUBERNETES_SERVICE_HOST",
							Value: "kubernetes",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: testSuperServiceAccountTokenSecretName,
					VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{
							SecretName: testSuperServiceAccountTokenSecretName,
						},
					},
				},
				{
					Name: "i-want-to-mount",
					VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{
							SecretName: testSuperServiceAccountTokenSecretName,
						},
					},
				},
			},
			HostAliases: []v1.HostAlias{
				{
					Hostnames: []string{"kubernetes", "kubernetes.default", "kubernetes.default.svc"},
				},
			},
		},
	}
}

func tenantServiceAccount(name, namespace, uid string) *v1.ServiceAccount {
	return &v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(uid),
		},
		Secrets: []v1.ObjectReference{
			{
				Name: testTenantServiceAccountTokenSecretName,
			},
		},
	}
}

func superService(name, namespace, uid string, clusterIP string) *v1.Service {
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				constants.LabelUID: uid,
			},
		},
	}
	if clusterIP != "" {
		svc.Spec.ClusterIP = clusterIP
	}
	return svc
}

func superSecret(name, namespace, uid string) *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				constants.LabelSecretUID: uid,
			},
		},
	}
}

func TestDWPodCreation(t *testing.T) {
	testTenant := &v1alpha1.VirtualCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "tenant-1",
			UID:       "7374a172-c35d-45b1-9c8e-bf5c5b614937",
		},
		Spec: v1alpha1.VirtualClusterSpec{},
		Status: v1alpha1.VirtualClusterStatus{
			Phase: v1alpha1.ClusterRunning,
		},
	}

	defaultClusterKey := conversion.ToClusterKey(testTenant)
	defaultVCName, defaultVCNamespace := testTenant.Name, testTenant.Namespace
	superDefaultNSName := conversion.ToSuperMasterNamespace(defaultClusterKey, "default")

	testcases := map[string]struct {
		ExistingObjectInSuper  []runtime.Object
		ExistingObjectInTenant []runtime.Object
		ExpectedCreatedPods    []*v1.Pod
		ExpectedError          string
	}{
		"new Pod": {
			ExistingObjectInSuper: []runtime.Object{
				superSecret("default-token-12345", superDefaultNSName, "s12345"),
				superService("kubernetes", superDefaultNSName, "12345", ""),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "default", "12345"),
				tenantSecret(testTenantServiceAccountTokenSecretName, "default", "s12345"),
				tenantServiceAccount("default", "default", "12345"),
			},
			ExpectedCreatedPods: []*v1.Pod{superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345")},
		},
		"load pod which under deletion": {
			ExistingObjectInSuper: []runtime.Object{},
			ExistingObjectInTenant: []runtime.Object{
				applyDeletionTimestampToPod(tenantPod("pod-1", "default", "12345"), time.Now(), 30),
			},
			ExpectedError: "",
		},
		"missing tenant service account token secret": {
			ExistingObjectInSuper: []runtime.Object{
				superService("kubernetes", superDefaultNSName, "12345", ""),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "default", "12345"),
				tenantServiceAccount("default", "default", "12345"),
			},
			ExpectedError: "failed to get vSecret",
		},
		"missing super service account token secret": {
			ExistingObjectInSuper: []runtime.Object{
				superService("kubernetes", superDefaultNSName, "12345", ""),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "default", "12345"),
				tenantSecret(testTenantServiceAccountTokenSecretName, "default", "s12345"),
				tenantServiceAccount("default", "default", "12345"),
			},
			ExpectedError: "failed to find sa secret from super master",
		},
		"multi tenant service account token secret": {
			ExistingObjectInSuper: []runtime.Object{
				superSecret("default-token-12345", superDefaultNSName, "s12345"),
				superService("kubernetes", superDefaultNSName, "12345", ""),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "default", "12345"),
				tenantSecret(testTenantServiceAccountTokenSecretName, "default", "s12345"),
				tenantSecret(testTenantServiceAccountTokenSecretName+"dup", "default", "s123456"),
				tenantServiceAccount("default", "default", "12345"),
			},
			ExpectedCreatedPods: []*v1.Pod{superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345")},
		},
		"multi service account token secret": {
			ExistingObjectInSuper: []runtime.Object{
				superSecret("default-token-12345", superDefaultNSName, "s12345"),
				superSecret("default-token-123456", superDefaultNSName, "s123456"),
				superService("kubernetes", superDefaultNSName, "12345", ""),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "default", "12345"),
				tenantSecret(testTenantServiceAccountTokenSecretName, "default", "s12345"),
				tenantSecret(testTenantServiceAccountTokenSecretName+"dup", "default", "s123456"),
				tenantServiceAccount("default", "default", "12345"),
			},
			ExpectedCreatedPods: []*v1.Pod{superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345")},
		},
		"without any services": {
			ExistingObjectInSuper: []runtime.Object{
				superSecret("default-token-12345", superDefaultNSName, "s12345"),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "default", "12345"),
				tenantSecret(testTenantServiceAccountTokenSecretName, "default", "s12345"),
				tenantServiceAccount("default", "default", "12345"),
			},
			ExpectedError: "service is not ready",
		},
		"only a dns service": {
			ExistingObjectInSuper: []runtime.Object{
				superSecret("default-token-12345", conversion.ToSuperMasterNamespace(defaultClusterKey, "kube-system"), "s12345"),
				superService(constants.TenantDNSServerServiceName, conversion.ToSuperMasterNamespace(defaultClusterKey, constants.TenantDNSServerNS), "12345", "192.168.0.10"),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "kube-system", "12345"),
				tenantSecret(testTenantServiceAccountTokenSecretName, "kube-system", "s12345"),
				tenantServiceAccount("default", "kube-system", "12345"),
			},
			ExpectedCreatedPods: []*v1.Pod{superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "kube-system", "12345")},
			ExpectedError:       "",
		},
		"new pod with nodeName": {
			ExistingObjectInSuper: []runtime.Object{
				superSecret("default-token-12345", superDefaultNSName, "s12345"),
				superService("kubernetes", superDefaultNSName, "12345", ""),
			},
			ExistingObjectInTenant: []runtime.Object{
				applyNodeNameToPod(tenantPod("pod-1", "default", "12345"), "i-xxxx"),
				tenantSecret(testTenantServiceAccountTokenSecretName, "default", "s12345"),
				tenantServiceAccount("default", "default", "12345"),
			},
		},
		"new Pod but already exists": {
			ExistingObjectInSuper: []runtime.Object{
				superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "default", "12345"),
			},
			ExpectedError: "",
		},
		"new Pod but existing different uid one": {
			ExistingObjectInSuper: []runtime.Object{
				superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "123456"),
			},
			ExistingObjectInTenant: []runtime.Object{
				tenantPod("pod-1", "default", "12345"),
			},
			ExpectedError: "delegated UID is different",
		},
	}

	for k, tc := range testcases {
		t.Run(k, func(t *testing.T) {
			actions, reconcileErr, err := util.RunDownwardSync(NewPodController, testTenant, tc.ExistingObjectInSuper, tc.ExistingObjectInTenant, tc.ExistingObjectInTenant[0], nil)
			if err != nil {
				t.Errorf("%s: error running downward sync: %v", k, err)
				return
			}

			if reconcileErr != nil {
				if tc.ExpectedError == "" {
					t.Errorf("expected no error, but got \"%v\"", reconcileErr)
				} else if !strings.Contains(reconcileErr.Error(), tc.ExpectedError) {
					t.Errorf("expected error msg \"%s\", but got \"%v\"", tc.ExpectedError, reconcileErr)
				}
			} else {
				if tc.ExpectedError != "" {
					t.Errorf("expected error msg \"%s\", but got empty", tc.ExpectedError)
				}
			}

			if len(tc.ExpectedCreatedPods) != len(actions) {
				t.Errorf("%s: Expected to create Pod %#v. Actual actions were: %#v", k, tc.ExpectedCreatedPods, actions)
				return
			}

			for i := range tc.ExpectedCreatedPods {
				action := actions[i]
				if !action.Matches("create", "pods") {
					t.Errorf("%s: Unexpected action %s", k, action)
				}
				createdPod := action.(core.CreateAction).GetObject().(*v1.Pod)

				bb, _ := json.Marshal(createdPod)
				fmt.Printf("==== %s\n\n\n\n", string(bb))

				bb, _ = json.Marshal(tc.ExpectedCreatedPods)
				fmt.Printf("=== %s", string(bb))

				if !equality.Semantic.DeepEqual(createdPod, tc.ExpectedCreatedPods[i]) {
					t.Errorf("%s: Expected %+v to be created, got %+v", k, tc.ExpectedCreatedPods, createdPod)
				}

			}
		})
	}
}

func TestDWPodDeletion(t *testing.T) {
	testTenant := &v1alpha1.VirtualCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "tenant-1",
			UID:       "7374a172-c35d-45b1-9c8e-bf5c5b614937",
		},
		Spec: v1alpha1.VirtualClusterSpec{},
		Status: v1alpha1.VirtualClusterStatus{
			Phase: v1alpha1.ClusterRunning,
		},
	}

	defaultClusterKey := conversion.ToClusterKey(testTenant)
	defaultVCName, defaultVCNamespace := testTenant.Name, testTenant.Namespace
	superDefaultNSName := conversion.ToSuperMasterNamespace(defaultClusterKey, "default")

	testcases := map[string]struct {
		ExistingObjectInSuper  []runtime.Object
		ExistingObjectInTenant []runtime.Object
		EnqueueObject          *v1.Pod
		ExpectedDeletedPods    []string
		ExpectedError          string
	}{
		"delete Pod": {
			ExistingObjectInSuper: []runtime.Object{
				superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"),
			},
			EnqueueObject:       tenantPod("pod-1", "default", "12345"),
			ExpectedDeletedPods: []string{superDefaultNSName + "/pod-1"},
		},
		"delete vPod and pPod is already running": {
			ExistingObjectInSuper: []runtime.Object{
				applyNodeNameToPod(superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"), "i-xxx"),
			},
			EnqueueObject:       tenantPod("pod-1", "default", "12345"),
			ExpectedDeletedPods: []string{superDefaultNSName + "/pod-1"},
		},
		"delete Pod but already gone": {
			ExistingObjectInSuper: []runtime.Object{},
			EnqueueObject:         tenantPod("pod-1", "default", "12345"),
			ExpectedDeletedPods:   []string{},
			ExpectedError:         "",
		},
		"delete Pod but existing different uid one": {
			ExistingObjectInSuper: []runtime.Object{
				superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "123456"),
			},
			EnqueueObject:       tenantPod("pod-1", "default", "12345"),
			ExpectedDeletedPods: []string{},
			ExpectedError:       "delegated UID is different",
		},
		"terminating vPod but running pPod": {
			ExistingObjectInSuper: []runtime.Object{
				superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"),
			},
			ExistingObjectInTenant: []runtime.Object{
				applyDeletionTimestampToPod(tenantPod("pod-1", "default", "12345"), time.Now(), 30),
			},
			EnqueueObject:       applyDeletionTimestampToPod(tenantPod("pod-1", "default", "12345"), time.Now(), 30),
			ExpectedDeletedPods: []string{superDefaultNSName + "/pod-1"},
			ExpectedError:       "",
		},
		"terminating vPod and terminating pPod": {
			ExistingObjectInSuper: []runtime.Object{
				applyDeletionTimestampToPod(superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"), time.Now(), 30),
			},
			ExistingObjectInTenant: []runtime.Object{
				applyDeletionTimestampToPod(tenantPod("pod-1", "default", "12345"), time.Now(), 30),
			},
			EnqueueObject:       applyDeletionTimestampToPod(tenantPod("pod-1", "default", "12345"), time.Now(), 30),
			ExpectedDeletedPods: []string{},
			ExpectedError:       "",
		},
	}

	for k, tc := range testcases {
		t.Run(k, func(t *testing.T) {
			actions, reconcileErr, err := util.RunDownwardSync(NewPodController, testTenant, tc.ExistingObjectInSuper, tc.ExistingObjectInTenant, tc.EnqueueObject, nil)
			if err != nil {
				t.Errorf("%s: error running downward sync: %v", k, err)
				return
			}

			if reconcileErr != nil {
				if tc.ExpectedError == "" {
					t.Errorf("expected no error, but got \"%v\"", reconcileErr)
				} else if !strings.Contains(reconcileErr.Error(), tc.ExpectedError) {
					t.Errorf("expected error msg \"%s\", but got \"%v\"", tc.ExpectedError, reconcileErr)
				}
			} else {
				if tc.ExpectedError != "" {
					t.Errorf("expected error msg \"%s\", but got empty", tc.ExpectedError)
				}
			}

			if len(tc.ExpectedDeletedPods) != len(actions) {
				t.Errorf("%s: Expected to delete pod %#v. Actual actions were: %#v", k, tc.ExpectedDeletedPods, actions)
				return
			}
			for i, expectedName := range tc.ExpectedDeletedPods {
				action := actions[i]
				if !action.Matches("delete", "pods") {
					t.Errorf("%s: Unexpected action %s", k, action)
				}
				fullName := action.(core.DeleteAction).GetNamespace() + "/" + action.(core.DeleteAction).GetName()
				if fullName != expectedName {
					t.Errorf("%s: Expected %s to be created, got %s", k, expectedName, fullName)
				}
			}
		})
	}
}

func applySpecToPod(pod *v1.Pod, spec *v1.PodSpec) *v1.Pod {
	pod.Spec = *spec.DeepCopy()
	return pod
}

func TestDWPodUpdate(t *testing.T) {
	testTenant := &v1alpha1.VirtualCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "tenant-1",
			UID:       "7374a172-c35d-45b1-9c8e-bf5c5b614937",
		},
		Spec: v1alpha1.VirtualClusterSpec{},
		Status: v1alpha1.VirtualClusterStatus{
			Phase: v1alpha1.ClusterRunning,
		},
	}

	defaultClusterKey := conversion.ToClusterKey(testTenant)
	defaultVCName, defaultVCNamespace := testTenant.Name, testTenant.Namespace
	spec1 := &v1.PodSpec{
		Containers: []v1.Container{
			{
				Image: "ngnix",
				Name:  "c-1",
			},
		},
		NodeName: "i-xxx",
	}

	spec2 := &v1.PodSpec{
		Containers: []v1.Container{
			{
				Image: "busybox",
				Name:  "c-1",
			},
		},
		NodeName: "i-xxx",
	}

	spec3 := &v1.PodSpec{
		Containers: []v1.Container{
			{
				Image: "ngnix",
				Name:  "c-1",
			},
			{
				Image: "ngnix2",
				Name:  "by-webhook",
			},
		},
		NodeName: "i-xxx",
	}

	testcases := map[string]struct {
		ExistingObjectInSuper  []runtime.Object
		ExistingObjectInTenant []runtime.Object
		ExpectedUpdatedPods    []runtime.Object
		ExpectedNoOperation    bool
		ExpectedError          string
	}{
		"no diff": {
			ExistingObjectInSuper: []runtime.Object{
				applySpecToPod(superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"), spec1),
			},
			ExistingObjectInTenant: []runtime.Object{
				applySpecToPod(tenantPod("pod-1", "default", "12345"), spec1),
			},
			ExpectedUpdatedPods: []runtime.Object{},
		},
		"diff in container": {
			ExistingObjectInSuper: []runtime.Object{
				applySpecToPod(superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"), spec1),
			},
			ExistingObjectInTenant: []runtime.Object{
				applySpecToPod(tenantPod("pod-1", "default", "12345"), spec2),
			},
			ExpectedUpdatedPods: []runtime.Object{
				applySpecToPod(superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"), spec2),
			},
		},
		"diff in container added by webhook": {
			ExistingObjectInSuper: []runtime.Object{
				applySpecToPod(superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"), spec3),
			},
			ExistingObjectInTenant: []runtime.Object{
				applySpecToPod(tenantPod("pod-1", "default", "12345"), spec1),
			},
			ExpectedNoOperation: true,
		},
		"diff exists but uid is wrong": {
			ExistingObjectInSuper: []runtime.Object{
				applySpecToPod(superPod(defaultClusterKey, defaultVCName, defaultVCNamespace, "pod-1", "default", "12345"), spec1),
			},
			ExistingObjectInTenant: []runtime.Object{
				applySpecToPod(tenantPod("pod-1", "default", "123456"), spec2),
			},
			ExpectedUpdatedPods: []runtime.Object{},
			ExpectedError:       "delegated UID is different",
		},
	}
	for k, tc := range testcases {
		t.Run(k, func(t *testing.T) {
			actions, reconcileErr, err := util.RunDownwardSync(NewPodController, testTenant, tc.ExistingObjectInSuper, tc.ExistingObjectInTenant, tc.ExistingObjectInTenant[0], nil)
			if err != nil {
				t.Errorf("%s: error running downward sync: %v", k, err)
				return
			}

			if reconcileErr != nil {
				if tc.ExpectedError == "" {
					t.Errorf("expected no error, but got \"%v\"", reconcileErr)
				} else if !strings.Contains(reconcileErr.Error(), tc.ExpectedError) {
					t.Errorf("expected error msg \"%s\", but got \"%v\"", tc.ExpectedError, reconcileErr)
				}
			} else {
				if tc.ExpectedError != "" {
					t.Errorf("expected error msg \"%s\", but got empty", tc.ExpectedError)
				}
			}

			if tc.ExpectedNoOperation {
				if len(actions) != 0 {
					t.Errorf("%s: Expect no operation, got %v", k, actions)
					return
				}
				return
			}

			if len(tc.ExpectedUpdatedPods) != len(actions) {
				t.Errorf("%s: Expected to update pod %#v. Actual actions were: %#v", k, tc.ExpectedUpdatedPods, actions)
				return
			}
			for i, obj := range tc.ExpectedUpdatedPods {
				action := actions[i]
				if !action.Matches("update", "pods") {
					t.Errorf("%s: Unexpected action %s", k, action)
				}
				actionObj := action.(core.UpdateAction).GetObject()
				if !equality.Semantic.DeepEqual(obj, actionObj) {
					t.Errorf("%s: Expected updated pod is %v, got %v", k, obj, actionObj)
				}
			}
		})
	}
}
