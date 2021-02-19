package validators

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-logr/logr"
	k8sadm "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	api "sigs.k8s.io/multi-tenancy/incubator/hnc/api/v1alpha2"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/config"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/forest"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/metadata"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/object"
	"sigs.k8s.io/multi-tenancy/incubator/hnc/internal/pkg/selectors"
)

// ObjectsServingPath is where the validator will run. Must be kept in sync with the
// kubebuilder markers below.
const (
	ObjectsServingPath = "/validate-objects"
)

// Note: the validating webhook FAILS OPEN. This means that if the webhook goes down, all further
// changes to the objects are allowed.
//
// +kubebuilder:webhook:path=/validate-objects,mutating=false,failurePolicy=ignore,groups="*",resources="*",verbs=create;update;delete,versions="*",name=objects.hnc.x-k8s.io

type Object struct {
	Log     logr.Logger
	Forest  *forest.Forest
	client  client.Client
	decoder *admission.Decoder
}

func (o *Object) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := o.Log.WithValues("resource", req.Resource, "ns", req.Namespace, "nm", req.Name, "op", req.Operation, "user", req.UserInfo.Username)

	// Before even looking at the objects, early-exit for any changes we shouldn't be involved in.
	// This reduces the chance we'll hose some aspect of the cluster we weren't supposed to touch.
	//
	// Firstly, skip namespaces we're excluded from (like kube-system).
	if config.EX[req.Namespace] {
		return allow("excluded namespace " + req.Namespace)
	}
	// Allow changes to the types that are not in propagate mode. This is to dynamically enable/disable
	// object webhooks based on the types configured in hncconfig. Since the current admission rules only
	// apply to propagated objects, we can disable object webhooks on all other non-propagate-mode types.
	if !o.isPropagateType(req.Kind) {
		return allow("Non-propagate-mode types")
	}
	// Finally, let the HNC SA do whatever it wants.
	if isHNCServiceAccount(&req.AdmissionRequest.UserInfo) {
		log.V(1).Info("Allowed change by HNC SA")
		return allow("HNC SA")
	}

	// Decode the old and new object, if we expect them to exist ("old" won't exist for creations,
	// while "new" won't exist for deletions).
	inst := &unstructured.Unstructured{}
	oldInst := &unstructured.Unstructured{}
	if req.Operation != k8sadm.Delete {
		if err := o.decoder.Decode(req, inst); err != nil {
			log.Error(err, "Couldn't decode req.Object", "raw", req.Object)
			return deny(metav1.StatusReasonBadRequest, err.Error())
		}
	}
	if req.Operation != k8sadm.Create {
		// See issue #688 and #889
		if req.Operation == k8sadm.Delete && req.OldObject.Raw == nil {
			return allow("cannot validate deletions in K8s 1.14")
		}
		if err := o.decoder.DecodeRaw(req.OldObject, oldInst); err != nil {
			log.Error(err, "Couldn't decode req.OldObject", "raw", req.OldObject)
			return deny(metav1.StatusReasonBadRequest, err.Error())
		}
	}

	// Run the actual logic.
	resp := o.handle(ctx, log, req.Operation, inst, oldInst)
	if !resp.Allowed {
		log.Info("Denied", "code", resp.Result.Code, "reason", resp.Result.Reason, "message", resp.Result.Message)
	} else {
		log.V(1).Info("Allowed", "message", resp.Result.Message)
	}
	return resp
}

func (o *Object) isPropagateType(gvk metav1.GroupVersionKind) bool {
	o.Forest.Lock()
	defer o.Forest.Unlock()

	ts := o.Forest.GetTypeSyncerFromGroupKind(schema.GroupKind{Group: gvk.Group, Kind: gvk.Kind})
	return ts != nil && ts.GetMode() == api.Propagate
}

// handle implements the non-webhook-y businesss logic of this validator, allowing it to be more
// easily unit tested (ie without constructing an admission.Request, setting up user infos, etc).
func (o *Object) handle(ctx context.Context, log logr.Logger, op k8sadm.Operation, inst, oldInst *unstructured.Unstructured) admission.Response {
	// Find out if the object was/is inherited, and where it's inherited from.
	oldSource, oldInherited := metadata.GetLabel(oldInst, api.LabelInheritedFrom)
	newSource, newInherited := metadata.GetLabel(inst, api.LabelInheritedFrom)

	// If the object wasn't and isn't inherited, we will check to see if the
	// source can be created without causing any conflict.
	if !oldInherited && !newInherited {
		// check if there is any invalid HNC annotation
		if msg := validateSelectorAnnot(inst); msg != "" {
			return deny(metav1.StatusReasonBadRequest, msg)
		}
		// check selector format
		// If this is a selector change, and the new selector is not valid, we'll deny this operation
		if err := validateSelectorChange(inst, oldInst); err != nil {
			msg := fmt.Sprintf("Invalid Kubernetes labelSelector: %s", err)
			return deny(metav1.StatusReasonBadRequest, msg)
		}
		if err := validateTreeSelectorChange(inst, oldInst); err != nil {
			msg := fmt.Sprintf("Invalid HNC %q value: %s", api.AnnotationTreeSelector, err)
			return deny(metav1.StatusReasonBadRequest, msg)
		}
		if err := validateNoneSelectorChange(inst, oldInst); err != nil {
			return deny(metav1.StatusReasonBadRequest, err.Error())
		}
		if msg := validateSelectorUniqueness(inst, oldInst); msg != "" {
			return deny(metav1.StatusReasonBadRequest, msg)
		}

		if yes, dnses := o.hasConflict(inst); yes {
			dnsesStr := strings.Join(dnses, "\n  * ")
			msg := fmt.Sprintf("\nCannot create %q (%s) in namespace %q because it would overwrite objects in the following descendant namespace(s):\n  * %s\nTo fix this, choose a different name for the object, or remove the conflicting objects from the above namespaces.", inst.GetName(), inst.GroupVersionKind(), inst.GetNamespace(), dnsesStr)
			return deny(metav1.StatusReasonConflict, msg)
		}
		return allow("source object")
	}
	// This is a propagated object.
	return o.handleInherited(ctx, op, newSource, oldSource, inst, oldInst)
}

func validateSelectorAnnot(inst *unstructured.Unstructured) string {
	annots := inst.GetAnnotations()
	for key, _ := range annots {
		// for example: segs = ["propagate.hnc.x-k8s.io", "select"]
		segs := strings.SplitN(key, "/", 2)
		// for example: prefix = ["propagate", "hnc.x-k8s.io"]
		prefix := strings.SplitN(segs[0], ".", 2)
		if len(prefix) < 2 || prefix[1] != api.MetaGroup {
			continue
		}
		msg := "invalid HNC exceptions annotation: %v, should be one of the following: " +
			api.AnnotationSelector + "; " + api.AnnotationTreeSelector + "; " +
			api.AnnotationNoneSelector
		// If this annotation is part of HNC metagroup, we check if the prefix value is valid
		if segs[0] != api.AnnotationPropagatePrefix {
			return fmt.Sprintf(msg, key)
		}
		// check if the suffix is valid by checking the whole annotation key
		if key != api.AnnotationSelector &&
			key != api.AnnotationTreeSelector &&
			key != api.AnnotationNoneSelector {
			return fmt.Sprintf(msg, key)
		}
	}
	return ""
}

func validateSelectorUniqueness(inst, oldInst *unstructured.Unstructured) string {
	sel := selectors.GetSelectorAnnotation(inst)
	treeSel := selectors.GetTreeSelectorAnnotation(inst)
	noneSel := selectors.GetNoneSelectorAnnotation(inst)

	oldSel := selectors.GetSelectorAnnotation(oldInst)
	oldTreeSel := selectors.GetTreeSelectorAnnotation(oldInst)
	oldNoneSel := selectors.GetNoneSelectorAnnotation(oldInst)

	isSelectorChange := oldSel != sel || oldTreeSel != treeSel || oldNoneSel != noneSel
	if !isSelectorChange {
		return ""
	}
	found := []string{}
	if sel != "" {
		found = append(found, api.AnnotationSelector)
	}
	if treeSel != "" {
		found = append(found, api.AnnotationTreeSelector)
	}
	if noneSel != "" {
		found = append(found, api.AnnotationNoneSelector)
	}
	if len(found) <= 1 {
		return ""
	}
	msg := "cannot have more than one selector at the same time, but got multiple: %v"
	return fmt.Sprintf(msg, strings.Join(found, ", "))
}

func validateSelectorChange(inst, oldInst *unstructured.Unstructured) error {
	oldSelectorStr := selectors.GetSelectorAnnotation(oldInst)
	newSelectorStr := selectors.GetSelectorAnnotation(inst)
	if newSelectorStr == "" || oldSelectorStr == newSelectorStr {
		return nil
	}
	_, err := selectors.GetSelector(inst)
	return err
}

func validateTreeSelectorChange(inst, oldInst *unstructured.Unstructured) error {
	oldSelectorStr := selectors.GetTreeSelectorAnnotation(oldInst)
	newSelectorStr := selectors.GetTreeSelectorAnnotation(inst)
	if newSelectorStr == "" || oldSelectorStr == newSelectorStr {
		return nil
	}
	_, err := selectors.GetTreeSelector(inst)
	return err
}

func validateNoneSelectorChange(inst, oldInst *unstructured.Unstructured) error {
	oldSelectorStr := selectors.GetNoneSelectorAnnotation(oldInst)
	newSelectorStr := selectors.GetNoneSelectorAnnotation(inst)
	if newSelectorStr == "" || oldSelectorStr == newSelectorStr {
		return nil
	}
	_, err := selectors.GetNoneSelector(inst)
	return err
}

func (o *Object) handleInherited(ctx context.Context, op k8sadm.Operation, newSource, oldSource string, inst, oldInst *unstructured.Unstructured) admission.Response {
	// Propagated objects cannot be created or deleted (except by the HNC SA, but the HNC SA
	// never gets this far in the validation). They *can* have their statuses updated, so
	// if this is an update, make sure that the canonical form of the object hasn't changed.
	switch op {
	case k8sadm.Create:
		return deny(metav1.StatusReasonForbidden, "Cannot create objects with the label \""+api.LabelInheritedFrom+"\"")

	case k8sadm.Delete:
		// There are few things more irritating in (K8s) life than having some stupid controller stop
		// your namespace from being deleted. If there's an object in here and we've decided that the
		// namespace should be deleted, then don't block anything!
		isDeleting, err := o.isDeletingNS(ctx, oldInst.GetNamespace())
		if err != nil {
			return deny(metav1.StatusReasonInternalError, "Cannot delete object propagated from namespace \""+oldSource+"\" with error: "+err.Error())
		}

		if !isDeleting {
			return deny(metav1.StatusReasonForbidden, "Cannot delete object propagated from namespace \""+oldSource+"\"")
		}

		return allow("allowing deletion of propagated object since namespace is being deleted")

	case k8sadm.Update:
		// If the values have changed, that's an illegal modification. This includes if the label is
		// added or deleted. Note that this label is *not* included in object.Canonical(), below, so we
		// need to check it manually.
		if newSource != oldSource {
			return deny(metav1.StatusReasonForbidden, "Cannot modify the label \""+api.LabelInheritedFrom+"\"")
		}

		// If the existing object has an inheritedFrom label, it's a propagated object. Any user changes
		// should be rejected. Note that object.Canonical does *not* compare any HNC labels or
		// annotations.
		if !reflect.DeepEqual(object.Canonical(inst), object.Canonical(oldInst)) {
			return deny(metav1.StatusReasonForbidden,
				"Cannot modify object propagated from namespace \""+oldSource+"\"")
		}

		return allow("no illegal updates to propagated object")
	}

	// If you get here, it means the webhook config is misconfigured to include an operation that we
	// actually don't support.
	return deny(metav1.StatusReasonInternalError, "unknown operation: "+string(op))
}

// validateDeletingNS validates if the namespace of the object is already being deleted
func (o *Object) isDeletingNS(ctx context.Context, ns string) (bool, error) {
	nsObj := &corev1.Namespace{}
	nnm := types.NamespacedName{Name: ns}
	if err := o.client.Get(ctx, nnm, nsObj); err != nil {
		// `IsNotFound` should never happen, but if for some bizarre reason the namespace appears to be deleted before the object,
		// we should allow the object to be deleted too.
		if errors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("while determining whether namespace %q is being deleted: %w", ns, err)
	}

	if !nsObj.DeletionTimestamp.IsZero() {
		return true, nil
	}

	return false, nil
}

// hasConflict checks if there's any conflicting objects in the descendants. Returns
// true and a list of conflicting descendants, if yes.
func (o *Object) hasConflict(inst *unstructured.Unstructured) (bool, []string) {
	o.Forest.Lock()
	defer o.Forest.Unlock()

	// If the instance is empty (for a delete operation) or it's not namespace-scoped,
	// there must be no conflict.
	if inst == nil || inst.GetNamespace() == "" {
		return false, nil
	}

	nm := inst.GetName()
	gvk := inst.GroupVersionKind()
	descs := o.Forest.Get(inst.GetNamespace()).DescendantNames()
	conflicts := []string{}

	// Get a list of conflicting descendants if there's any.
	for _, desc := range descs {
		if o.Forest.Get(desc).HasSourceObject(gvk, nm) {
			// If the user have chosen not to propagate the object to this descendant,
			// there shouldn't be any conflict reported here
			nsLabels := o.Forest.Get(inst.GetNamespace()).GetLabels()
			if ok, _ := selectors.ShouldPropagate(inst, nsLabels); ok {
				conflicts = append(conflicts, desc)
			}
		}
	}

	return len(conflicts) != 0, conflicts
}

func (o *Object) InjectClient(c client.Client) error {
	o.client = c
	return nil
}

func (o *Object) InjectDecoder(d *admission.Decoder) error {
	o.decoder = d
	return nil
}
