package validators

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	authn "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	api "sigs.k8s.io/multi-tenancy/incubator/hnc/api/v1alpha2"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/config"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/foresttest"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/reconcilers"
)

func TestStructure(t *testing.T) {
	f := foresttest.Create("-a-") // a <- b; c
	h := &Hierarchy{Forest: f}
	l := zap.New()
	// For this unit test, we only set `kube-system` as an excluded namespace.
	config.ExcludedNamespaces = map[string]bool{"kube-system": true}

	tests := []struct {
		name        string
		nnm         string
		pnm         string
		fail        bool
		msgContains string
	}{
		{name: "ok", nnm: "a", pnm: "c"},
		{name: "missing parent", nnm: "a", pnm: "brumpf", fail: true, msgContains: "does not exist"},
		{name: "self-cycle", nnm: "a", pnm: "a", fail: true, msgContains: "Illegal parent"},
		{name: "other cycle", nnm: "a", pnm: "b", fail: true, msgContains: "Illegal parent"},
		// Since we only set `kube-system` as excluded namespaces for this test, we
		// should see denial message of excluded namespace for `kube-system`. As for
		// `kube-public`, we will see missing parent/child instead of excluded
		// namespaces denial message for it.
		{name: "exclude parent kube-system", nnm: "a", pnm: "kube-system", fail: true, msgContains: "Cannot set the parent to the excluded namespace"},
		{name: "missing parent kube-public", nnm: "a", pnm: "kube-public", fail: true, msgContains: "does not exist"},
		{name: "exclude child kube-system", nnm: "kube-system", pnm: "a", fail: true, msgContains: "Cannot set the excluded namespace"},
		{name: "missing child kube-public", nnm: "kube-public", pnm: "a", fail: true, msgContains: "HNC has not reconciled namespace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			g := NewWithT(t)
			hc := &api.HierarchyConfiguration{Spec: api.HierarchyConfigurationSpec{Parent: tc.pnm}}
			hc.ObjectMeta.Name = api.Singleton
			hc.ObjectMeta.Namespace = tc.nnm
			req := &request{hc: hc}

			// Test
			got := h.handle(context.Background(), l, req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).ShouldNot(Equal(tc.fail))
			g.Expect(got.Result.Message).Should(ContainSubstring(tc.msgContains))
		})
	}
}

func TestChangeParentOnManagedBy(t *testing.T) {
	f := foresttest.Create("-a-c") // a <- b; c <- d
	h := &Hierarchy{Forest: f}
	l := zap.New()

	// Make c and d external namespaces
	f.Get("c").ExternalTreeLabels = map[string]int{"c" + api.LabelTreeDepthSuffix: 0}
	f.Get("d").ExternalTreeLabels = map[string]int{"d" + api.LabelTreeDepthSuffix: 0}

	// These cases test changing parent for internal or external namespaces, described
	// in the table at https://bit.ly/hnc-external-hierarchy#heading=h.z9mkbslfq41g
	// with other cases covered in the namespace_test.go.
	tests := []struct {
		name string
		nnm  string
		pnm  string
		fail bool
	}{
		{name: "ok: change internal namespace parent from none to existing", nnm: "a", pnm: "c"},
		{name: "ok: change internal namespace existing parent", nnm: "b", pnm: "c"},
		{name: "ok: change internal namespace parent from existing to none", nnm: "b", pnm: ""},
		{name: "not ok: change external namespace parent from none to existing", nnm: "c", pnm: "a", fail: true},
		{name: "not ok: change external namespace existing parent", nnm: "d", pnm: "a", fail: true},
		{name: "ok: change external namespace parent from existing to none", nnm: "d", pnm: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			g := NewWithT(t)
			hc := &api.HierarchyConfiguration{Spec: api.HierarchyConfigurationSpec{Parent: tc.pnm}}
			hc.ObjectMeta.Name = api.Singleton
			hc.ObjectMeta.Namespace = tc.nnm
			req := &request{hc: hc}

			// Test
			got := h.handle(context.Background(), l, req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).ShouldNot(Equal(tc.fail))
		})
	}
}

func TestChangeParentWithConflict(t *testing.T) {
	f := foresttest.Create("-a-c") // a <- b; c <- d

	// Set secret to "Propagate" mode. (Use Secret in this test because the test
	// forest doesn't have Role or RoleBinding by default either. We can also create
	// secret by existing `createSecret()` function.)
	or := &reconcilers.ObjectReconciler{
		GVK:  schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"},
		Mode: api.Propagate,
	}
	f.AddTypeSyncer(or)

	// Create secrets with the same name in namespace 'a' and 'd'.
	createSecret("conflict", "a", f)
	createSecret("conflict", "d", f)

	h := &Hierarchy{Forest: f}
	l := zap.New()

	tests := []struct {
		name string
		nnm  string
		pnm  string
		fail bool
	}{
		{name: "conflict in itself and the new parent", nnm: "a", pnm: "d", fail: true},
		{name: "conflict in itself and a new ancestor (not the parent)", nnm: "d", pnm: "b", fail: true},
		{name: "ok: no conflict in ancestors", nnm: "a", pnm: "c"},
		{name: "conflict in subtree leaf and the new parent", nnm: "c", pnm: "a", fail: true},
		{name: "conflict in subtree leaf and a new ancestor (not the parent)", nnm: "c", pnm: "b", fail: true},
		{name: "ok: set a namespace as root", nnm: "d", pnm: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			g := NewWithT(t)
			hc := &api.HierarchyConfiguration{Spec: api.HierarchyConfigurationSpec{Parent: tc.pnm}}
			hc.ObjectMeta.Name = api.Singleton
			hc.ObjectMeta.Namespace = tc.nnm
			req := &request{hc: hc}

			// Test
			got := h.handle(context.Background(), l, req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Allowed).ShouldNot(Equal(tc.fail))
		})
	}
}

func TestAuthz(t *testing.T) {
	tests := []struct {
		name   string
		server fakeServer
		forest string
		nm     string
		to     string
		code   int32 // defaults to 0 (success)
	}{
		{name: "no permission in tree", forest: "-aab", nm: "d", to: "c", code: 401},                                 // a <- (b <- d, c)
		{name: "permission on root in tree", forest: "-aab", nm: "d", to: "c", server: "a"},                          // a <- (b <- d, c)
		{name: "permission on parents but not root", forest: "-aabd", nm: "e", to: "c", server: "bc", code: 401},     // a <- (b <- d <- e, c)
		{name: "permission on dst only", forest: "--a", nm: "c", to: "b", server: "b", code: 401},                    // a <- c; b
		{name: "permission on cur root only", forest: "--a", nm: "c", to: "b", server: "a", code: 401},               // a <- b; b
		{name: "permission on parents, but not cur root", forest: "-a-b", nm: "d", to: "c", server: "bc", code: 401}, // a <- b <- d; c
		{name: "permission on dst and cur root", forest: "-a-b", nm: "d", to: "c", server: "ac"},                     // a <- b <- d; c
		{name: "permission on mrca", forest: "-abbc", nm: "e", to: "d", server: "b"},                                 // a <- b <- (c <- e, d)
		{name: "unsynced parent", forest: "-z", nm: "b", to: "a", server: "a", code: 503},                            // a; z <- b (z hasn't been synced)
		{name: "missing parent", forest: "-z", nm: "b", to: "a", server: "a:z"},                                      // a; z <- b (z is missing)
		{name: "missing ancestor", forest: "z-a", nm: "c", to: "b", server: "ab", code: 403},                         // z <- a <- c; b (z hasn't been synced)
		{name: "unsynced ancestor", forest: "z-a", nm: "c", to: "b", server: "ab:z", code: 403},                      // z <- a <- c; b (z is missing)
		{name: "member of cycle (all permission)", forest: "cab", nm: "c", to: "", server: "abc"},                    // a,b,c in cycle
		{name: "member of cycle (no permission)", forest: "cab", nm: "c", to: "", server: "", code: 401},             // a,b,c in cycle
		{name: "descendant of cycle", forest: "baa", nm: "c", to: "b", server: "ab", code: 403},                      // c -> a <-> b
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Setup
			g := NewWithT(t)
			f := foresttest.Create(tc.forest)
			h := &Hierarchy{Forest: f, server: tc.server}
			l := zap.New()

			// Create request
			hc := &api.HierarchyConfiguration{Spec: api.HierarchyConfigurationSpec{Parent: tc.to}}
			hc.ObjectMeta.Name = api.Singleton
			hc.ObjectMeta.Namespace = tc.nm
			req := &request{hc: hc, ui: &authn.UserInfo{Username: "jen"}}

			// Test
			got := h.handle(context.Background(), l, req)

			// Report
			logResult(t, got.AdmissionResponse.Result)
			g.Expect(got.AdmissionResponse.Result.Code).Should(Equal(tc.code))
		})
	}
}

func logResult(t *testing.T, result *metav1.Status) {
	t.Logf("Got reason %q, code %d, msg %q", result.Reason, result.Code, result.Message)
}

// fakeServer implements serverClient. It's implemented as a string separated by a colon (":") with
// the following meanings:
// * Anything *before* the colon passes the IsAdmin check
// * Anything *after* the colon *fails* the Exists check
// If the colon is missing, it's assumed to come at the end of the string
type fakeServer string

func (f fakeServer) IsAdmin(_ context.Context, _ *authn.UserInfo, nnm string) (bool, error) {
	for _, n := range f {
		if nnm == string(n) {
			return true, nil
		}
		if n == ':' {
			return false, nil
		}
	}
	return false, nil
}

func (f fakeServer) Exists(_ context.Context, nnm string) (bool, error) {
	foundColon := false
	for _, n := range f {
		if n == ':' {
			foundColon = true
			continue
		}
		if !foundColon {
			continue
		}
		if nnm == string(n) {
			return false, nil
		}
	}
	return true, nil
}
