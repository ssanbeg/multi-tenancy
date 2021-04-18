package validators

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	k8sadm "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/config"

	api "sigs.k8s.io/multi-tenancy/incubator/hnc/api/v1alpha2"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/forest"
)

// NamespaceServingPath is where the validator will run. Must be kept in sync with the
// kubebuilder markers below.
const (
	NamespaceServingPath = "/validate-v1-namespace"
)

// Note: the validating webhook FAILS CLOSE. This means that if the webhook goes down, all further
// changes are forbidden.
//
// +kubebuilder:webhook:admissionReviewVersions=v1;v1beta1,path=/validate-v1-namespace,mutating=false,failurePolicy=fail,groups="",resources=namespaces,sideEffects=None,verbs=delete;create;update,versions=v1,name=namespaces.hnc.x-k8s.io

type Namespace struct {
	Log     logr.Logger
	Forest  *forest.Forest
	decoder *admission.Decoder
}

// nsRequest defines the aspects of the admission.Request that we care about.
type nsRequest struct {
	ns *corev1.Namespace
	op k8sadm.Operation
}

// Handle implements the validation webhook.
func (v *Namespace) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := v.Log.WithValues("nm", req.Name, "op", req.Operation, "user", req.UserInfo.Username)
	// Early exit since the HNC SA can do whatever it wants.
	if isHNCServiceAccount(&req.AdmissionRequest.UserInfo) {
		log.V(1).Info("Allowed change by HNC SA")
		return allow("HNC SA")
	}

	decoded, err := v.decodeRequest(log, req)
	if err != nil {
		log.Error(err, "Couldn't decode request")
		return deny(metav1.StatusReasonBadRequest, err.Error())
	}
	if decoded == nil {
		// https://github.com/kubernetes-sigs/multi-tenancy/issues/688
		return allow("")
	}

	resp := v.handle(decoded)
	if !resp.Allowed {
		log.Info("Denied", "code", resp.Result.Code, "reason", resp.Result.Reason, "message", resp.Result.Message)
	} else {
		log.V(1).Info("Allowed", "message", resp.Result.Message)
	}
	return resp
}

// handle implements the non-boilerplate logic of this validator, allowing it to be more easily unit
// tested (ie without constructing a full admission.Request).
func (v *Namespace) handle(req *nsRequest) admission.Response {
	v.Forest.Lock()
	defer v.Forest.Unlock()

	ns := v.Forest.Get(req.ns.Name)

	switch req.op {
	case k8sadm.Create:
		if rsp := v.illegalExcludedNamespaceLabel(req); !rsp.Allowed {
			return rsp
		}
		// This check only applies to the Create operation since namespace name
		// cannot be updated.
		if rsp := v.nameExistsInExternalHierarchy(req); !rsp.Allowed {
			return rsp
		}
	case k8sadm.Update:
		if rsp := v.illegalExcludedNamespaceLabel(req); !rsp.Allowed {
			return rsp
		}
		// This check only applies to the Update operation. Creating a namespace
		// with external manager is allowed and we will prevent this conflict by not
		// allowing setting a parent when validating the HierarchyConfiguration.
		if rsp := v.conflictBetweenParentAndExternalManager(req, ns); !rsp.Allowed {
			return rsp
		}
	case k8sadm.Delete:
		if rsp := v.cannotDeleteSubnamespace(req); !rsp.Allowed {
			return rsp
		}
		if rsp := v.illegalCascadingDeletion(ns); !rsp.Allowed {
			return rsp
		}
	}

	return allow("")
}

func (v *Namespace) illegalExcludedNamespaceLabel(req *nsRequest) admission.Response {
	for l := range req.ns.Labels {
		if l == api.LabelExcludedNamespace && !config.ExcludedNamespaces[req.ns.Name] {
			// Note: this only blocks the request if it has a newly added illegal
			// excluded-namespace label because existing illegal excluded-namespace
			// label should have already been removed by our reconciler. For example,
			// even when the VWHConfiguration is removed, adding the label to a non-
			// excluded namespace would pass but the label is immediately removed; when
			// the VWHConfiguration is there but the reconcilers are down, any request
			// gets denied anyway.
			msg := fmt.Sprintf("You cannot exclude this namespace using the %q label. See https://github.com/kubernetes-sigs/multi-tenancy/blob/master/incubator/hnc/docs/user-guide/concepts.md#excluded-namespace-label for detail.", api.LabelExcludedNamespace)
			return deny(metav1.StatusReasonForbidden, msg)
		}
	}
	return allow("")
}

func (v *Namespace) nameExistsInExternalHierarchy(req *nsRequest) admission.Response {
	for _, nm := range v.Forest.GetNamespaceNames() {
		if _, ok := v.Forest.Get(nm).ExternalTreeLabels[req.ns.Name]; ok {
			msg := fmt.Sprintf("The namespace name %q is reserved by the external hierarchy manager %q.", req.ns.Name, v.Forest.Get(nm).Manager)
			return deny(metav1.StatusReasonAlreadyExists, msg)
		}
	}
	return allow("")
}

func (v *Namespace) conflictBetweenParentAndExternalManager(req *nsRequest, ns *forest.Namespace) admission.Response {
	mgr := req.ns.Annotations[api.AnnotationManagedBy]
	if mgr != "" && mgr != api.MetaGroup && ns.Parent() != nil {
		msg := fmt.Sprintf("Namespace %q is a child of %q. Namespaces with parents defined by HNC cannot also be managed externally. "+
			"To manage this namespace with %q, first make it a root in HNC.", ns.Name(), ns.Parent().Name(), mgr)
		return deny(metav1.StatusReasonForbidden, msg)
	}
	return allow("")
}

func (v *Namespace) cannotDeleteSubnamespace(req *nsRequest) admission.Response {
	parent := req.ns.Annotations[api.SubnamespaceOf]
	// Early exit if the namespace is not a subnamespace.
	if parent == "" {
		return allow("")
	}

	// If the anchor doesn't exist, we want to allow it to be deleted anyway.
	// See issue https://github.com/kubernetes-sigs/multi-tenancy/issues/847.
	anchorExists := v.Forest.Get(parent).HasAnchor(req.ns.Name)
	if anchorExists {
		msg := fmt.Sprintf("The namespace %s is a subnamespace. Please delete the anchor from the parent namespace %s to delete the subnamespace.", req.ns.Name, parent)
		return deny(metav1.StatusReasonForbidden, msg)
	}
	return allow("")
}

func (v *Namespace) illegalCascadingDeletion(ns *forest.Namespace) admission.Response {
	if ns.AllowsCascadingDeletion() {
		return allow("")
	}

	for _, cnm := range ns.ChildNames() {
		if v.Forest.Get(cnm).IsSub {
			msg := "This namespaces contains subnamespaces. Please remove all subnamespaces before deleting this namespace, or set 'allowCascadingDeletion' to delete them automatically."
			return deny(metav1.StatusReasonForbidden, msg)
		}
	}
	return allow("no subnamespaces found")
}

// decodeRequest gets the information we care about into a simple struct that's easy to both a) use
// and b) factor out in unit tests.
func (v *Namespace) decodeRequest(log logr.Logger, in admission.Request) (*nsRequest, error) {
	ns := &corev1.Namespace{}
	var err error
	// For DELETE request, use DecodeRaw() from req.OldObject, since Decode() only uses req.Object,
	// which will be empty for a DELETE request.
	if in.Operation == k8sadm.Delete {
		if in.OldObject.Raw == nil {
			// See https://github.com/kubernetes-sigs/multi-tenancy/issues/688. OldObject can be nil in
			// K8s 1.14 and earlier.
			return nil, nil
		}
		log.V(1).Info("Decoding a delete request.")
		err = v.decoder.DecodeRaw(in.OldObject, ns)
	} else {
		err = v.decoder.Decode(in, ns)
	}
	if err != nil {
		return nil, err
	}

	return &nsRequest{
		ns: ns,
		op: in.Operation,
	}, nil
}

func (v *Namespace) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}
