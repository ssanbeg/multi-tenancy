package reconcilers_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	api "sigs.k8s.io/multi-tenancy/incubator/hnc/api/v1alpha2"
)

// GVKs maps a resource to its corresponding GVK.
var GVKs = map[string]schema.GroupVersionKind{
	"secrets":               {Group: "", Version: "v1", Kind: "Secret"},
	api.RoleResource:        {Group: api.RBACGroup, Version: "v1", Kind: api.RoleKind},
	api.RoleBindingResource: {Group: api.RBACGroup, Version: "v1", Kind: api.RoleBindingKind},
	"networkpolicies":       {Group: "networking.k8s.io", Version: "v1", Kind: "NetworkPolicy"},
	"resourcequotas":        {Group: "", Version: "v1", Kind: "ResourceQuota"},
	"limitranges":           {Group: "", Version: "v1", Kind: "LimitRange"},
	"configmaps":            {Group: "", Version: "v1", Kind: "ConfigMap"},
	// crontabs is a custom resource.
	"crontabs": {Group: "stable.example.com", Version: "v1", Kind: "CronTab"},
}

// createdObjects keeps track of objects created out of the makeObject function.
// This gives us a reflection of what's stored in the API server so that we can
// clean it up properly when cleanupObjects is called.
var createdObjects = []*unstructured.Unstructured{}

func newHierarchy(nm string) *api.HierarchyConfiguration {
	hier := &api.HierarchyConfiguration{}
	hier.ObjectMeta.Namespace = nm
	hier.ObjectMeta.Name = api.Singleton
	return hier
}

func getHierarchy(ctx context.Context, nm string) *api.HierarchyConfiguration {
	nnm := types.NamespacedName{Namespace: nm, Name: api.Singleton}
	hier := &api.HierarchyConfiguration{}
	if err := k8sClient.Get(ctx, nnm, hier); err != nil {
		GinkgoT().Logf("Error fetching hierarchy for %s: %s", nm, err)
	}
	return hier
}

func canGetHierarchy(ctx context.Context, nm string) func() bool {
	return func() bool {
		nnm := types.NamespacedName{Namespace: nm, Name: api.Singleton}
		hier := &api.HierarchyConfiguration{}
		if err := k8sClient.Get(ctx, nnm, hier); err != nil {
			return false
		}
		return true
	}
}

func updateHierarchy(ctx context.Context, h *api.HierarchyConfiguration) {
	if h.CreationTimestamp.IsZero() {
		ExpectWithOffset(1, k8sClient.Create(ctx, h)).Should(Succeed())
	} else {
		ExpectWithOffset(1, k8sClient.Update(ctx, h)).Should(Succeed())
	}
}

func tryUpdateHierarchy(ctx context.Context, h *api.HierarchyConfiguration) error {
	if h.CreationTimestamp.IsZero() {
		return k8sClient.Create(ctx, h)
	} else {
		return k8sClient.Update(ctx, h)
	}
}

func getLabel(ctx context.Context, from, label string) func() string {
	return func() string {
		ns := getNamespace(ctx, from)
		val, _ := ns.GetLabels()[label]
		return val
	}
}

func hasChild(ctx context.Context, nm, cnm string) func() bool {
	return func() bool {
		children := getHierarchy(ctx, nm).Status.Children
		for _, c := range children {
			if c == cnm {
				return true
			}
		}
		return false
	}
}

// Namespaces are named "a-<rand>", "b-<rand>", etc
func createNSes(ctx context.Context, num int) []string {
	nms := []string{}
	for i := 0; i < num; i++ {
		nm := createNS(ctx, fmt.Sprintf("%c", 'a'+i))
		nms = append(nms, nm)
	}
	return nms
}

func addNamespaceLabel(ctx context.Context, nm, k, v string) {
	ns := getNamespace(ctx, nm)
	l := ns.Labels
	l[k] = v
	ns.SetLabels(l)
	updateNamespace(ctx, ns)
}

func removeNamespaceLabel(ctx context.Context, nm, k string) {
	ns := getNamespace(ctx, nm)
	l := ns.Labels
	delete(l, k)
	ns.SetLabels(l)
	updateNamespace(ctx, ns)
}

func updateNamespace(ctx context.Context, ns *corev1.Namespace) {
	ExpectWithOffset(1, k8sClient.Update(ctx, ns)).Should(Succeed())
}

func setParent(ctx context.Context, nm, pnm string) {
	var oldPNM string
	GinkgoT().Logf("Changing parent of %s to %s", nm, pnm)
	EventuallyWithOffset(1, func() error {
		hier := newOrGetHierarchy(ctx, nm)
		oldPNM = hier.Spec.Parent
		hier.Spec.Parent = pnm
		return tryUpdateHierarchy(ctx, hier) // can fail if a reconciler updates the hierarchy
	}).Should(Succeed(), "When setting parent of %s to %s", nm, pnm)
	if oldPNM != "" {
		EventuallyWithOffset(1, func() []string {
			pHier := getHierarchy(ctx, oldPNM)
			return pHier.Status.Children
		}).ShouldNot(ContainElement(nm), "Verifying %s is no longer a child of %s", nm, oldPNM)
	}
	if pnm != "" {
		EventuallyWithOffset(1, func() []string {
			pHier := getHierarchy(ctx, pnm)
			return pHier.Status.Children
		}).Should(ContainElement(nm), "Verifying %s is now a child of %s", nm, pnm)
	}
}

func getNamespace(ctx context.Context, nm string) *corev1.Namespace {
	return getNamespaceWithOffset(1, ctx, nm)
}

func getNamespaceWithOffset(offset int, ctx context.Context, nm string) *corev1.Namespace {
	nnm := types.NamespacedName{Name: nm}
	ns := &corev1.Namespace{}
	EventuallyWithOffset(offset+1, func() error {
		return k8sClient.Get(ctx, nnm, ns)
	}).Should(Succeed())
	return ns
}

// createNS is a convenience function to create a namespace and wait for its singleton to be
// created. It's used in other tests in this package, but basically duplicates the code in this test
// (it didn't originally). TODO: refactor.
func createNS(ctx context.Context, prefix string) string {
	nm := createNSName(prefix)

	// Create the namespace
	ns := &corev1.Namespace{}
	ns.Name = nm
	Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
	return nm
}

// createNSName generates random namespace names. Namespaces are never deleted in test-env because
// the building Namespace controller (which finalizes namespaces) doesn't run; I searched Github and
// found that everyone who was deleting namespaces was *also* very intentionally generating random
// names, so I guess this problem is widespread.
func createNSName(prefix string) string {
	suffix := make([]byte, 10)
	rand.Read(suffix)
	return fmt.Sprintf("%s-%x", prefix, suffix)
}

// createNSWithLabel has similar function to createNS with label as additional parameter
func createNSWithLabel(ctx context.Context, prefix string, label map[string]string) string {
	nm := createNSName(prefix)

	// Create the namespace
	ns := &corev1.Namespace{}
	ns.SetLabels(label)
	ns.Name = nm
	Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
	return nm
}

// createNSWithLabelAnnotation has similar function to createNS with label and annotation
// as additional parameters.
func createNSWithLabelAnnotation(ctx context.Context, prefix string, l map[string]string, a map[string]string) string {
	nm := createNSName(prefix)

	// Create the namespace
	ns := &corev1.Namespace{}
	ns.SetLabels(l)
	ns.SetAnnotations(a)
	ns.Name = nm
	Expect(k8sClient.Create(ctx, ns)).Should(Succeed())
	return nm
}

func updateHNCConfig(ctx context.Context, c *api.HNCConfiguration) error {
	if c.CreationTimestamp.IsZero() {
		if err := k8sClient.Create(ctx, c); err != nil {
			return fmt.Errorf("while creating HNCConfiguration %q: %w", c.GetName(), err)
		}
	} else {
		if err := k8sClient.Update(ctx, c); err != nil {
			return fmt.Errorf("while updating HNCConfiguration %q: %w", c.GetName(), err)
		}
	}
	return nil
}

func resetHNCConfigToDefault(ctx context.Context) {
	EventuallyWithOffset(1, func() error {
		c, err := getHNCConfig(ctx)
		if err != nil {
			return err
		}
		c.Spec = api.HNCConfigurationSpec{}
		c.Status = api.HNCConfigurationStatus{}
		return k8sClient.Update(ctx, c)
	}).Should(Succeed(), "While resetting HNC config")
}

func getHNCConfig(ctx context.Context) (*api.HNCConfiguration, error) {
	return getHNCConfigWithName(ctx, api.HNCConfigSingleton)
}

func getHNCConfigWithName(ctx context.Context, nm string) (*api.HNCConfiguration, error) {
	nnm := types.NamespacedName{Name: nm}
	config := &api.HNCConfiguration{}
	if err := k8sClient.Get(ctx, nnm, config); err != nil {
		return nil, fmt.Errorf("while reading HNCConfiguration %q: %w", nm, err)
	}
	return config, nil
}

func addToHNCConfig(ctx context.Context, group, resource string, mode api.SynchronizationMode) {
	EventuallyWithOffset(1, func() error {
		c, err := getHNCConfig(ctx)
		if err != nil {
			return err
		}
		spec := api.ResourceSpec{Group: group, Resource: resource, Mode: mode}
		c.Spec.Resources = append(c.Spec.Resources, spec)
		return updateHNCConfig(ctx, c)
	}).Should(Succeed(), "While adding %s/%s=%s to HNC config", group, resource, mode)
}

// hasObject returns true if a namespace contains a specific object of the given kind.
//  The kind and its corresponding GVK should be included in the GVKs map.
func hasObject(ctx context.Context, resource string, nsName, name string) func() bool {
	// `Eventually` only works with a fn that doesn't take any args.
	return func() bool {
		_, err := getObject(ctx, resource, nsName, name)
		return err == nil
	}
}

func getObject(ctx context.Context, resource string, nsName, name string) (*unstructured.Unstructured, error) {
	nnm := types.NamespacedName{Namespace: nsName, Name: name}
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	err := k8sClient.Get(ctx, nnm, inst)
	return inst, err
}

// makeObject creates an empty object of the given kind in a specific namespace. The kind and
// its corresponding GVK should be included in the GVKs map.
func makeObject(ctx context.Context, resource string, nsName, name string) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	inst.SetNamespace(nsName)
	inst.SetName(name)
	ExpectWithOffset(1, k8sClient.Create(ctx, inst)).Should(Succeed(), "When creating %s %s/%s", resource, nsName, name)
	createdObjects = append(createdObjects, inst)
}

// makeObjectWithAnnotation creates an empty object with annotation given kind in a specific
// namespace. The kind and its corresponding GVK should be included in the GVKs map.
func makeObjectWithAnnotation(ctx context.Context, resource string, nsName,
	name string, a map[string]string) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	inst.SetNamespace(nsName)
	inst.SetName(name)
	inst.SetAnnotations(a)
	ExpectWithOffset(1, k8sClient.Create(ctx, inst)).Should(Succeed())
	createdObjects = append(createdObjects, inst)
}

// updateObjectWithAnnotation gets an object given it's kind, nsName and name, adds the annotation
// and updates this object
func updateObjectWithAnnotation(ctx context.Context, resource string, nsName,
	name string, a map[string]string) error {
	nnm := types.NamespacedName{Namespace: nsName, Name: name}
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	err := k8sClient.Get(ctx, nnm, inst)
	if err != nil {
		return err
	}
	inst.SetAnnotations(a)
	return k8sClient.Update(ctx, inst)
}

// deleteObject deletes an object of the given kind in a specific namespace. The kind and
// its corresponding GVK should be included in the GVKs map.
func deleteObject(ctx context.Context, resource string, nsName, name string) {
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	inst.SetNamespace(nsName)
	inst.SetName(name)
	EventuallyWithOffset(1, func() bool {
		return errors.IsNotFound(k8sClient.Delete(ctx, inst))
	}).Should(BeTrue())
}

// cleanupObjects makes a best attempt to cleanup all objects created from makeObject.
func cleanupObjects(ctx context.Context) {
	for _, obj := range createdObjects {
		err := k8sClient.Delete(ctx, obj)
		if err != nil {
			Eventually(errors.IsNotFound(k8sClient.Delete(ctx, obj))).Should(BeTrue())
		}
	}
	createdObjects = []*unstructured.Unstructured{}
}

// objectInheritedFrom returns the name of the namespace where a specific object of a given kind
// is propagated from or an empty string if the object is not a propagated object. The kind and
// its corresponding GVK should be included in the GVKs map.
func objectInheritedFrom(ctx context.Context, resource string, nsName, name string) string {
	nnm := types.NamespacedName{Namespace: nsName, Name: name}
	inst := &unstructured.Unstructured{}
	inst.SetGroupVersionKind(GVKs[resource])
	if err := k8sClient.Get(ctx, nnm, inst); err != nil {
		// should have been caught above
		return err.Error()
	}
	if inst.GetLabels() == nil {
		return ""
	}
	lif, _ := inst.GetLabels()[api.LabelInheritedFrom]
	return lif
}

// replaceStrings returns a copy of str with all non-overlapping instances of the keys in table
// replaced by values in table
func replaceStrings(str string, table map[string]string) string {
	for key, val := range table {
		str = strings.ReplaceAll(str, key, val)
	}
	return str
}
