package hook

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cybozu-go/topolvm"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// defaultSize volume will be created for PVC w/o capacity requests.
const defaultSize = 1 << 30

var pmLogger = logf.Log.WithName("pod-mutator")

// +kubebuilder:webhook:path=/mutate,mutating=true,failurePolicy=fail,groups="",resources=pods,verbs=create,versions=v1,name=topolvm-hook
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch

// podMutator mutates pods using PVC for TopoLVM.
type podMutator struct {
	client  client.Client
	decoder *admission.Decoder
}

// Handle implements admission.Handler interface.
func (m podMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}
	err := m.decoder.Decode(req, pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if len(pod.Spec.Containers) == 0 {
		return admission.Denied("pod has no containers")
	}

	// short cut
	if len(pod.Spec.Volumes) == 0 {
		return admission.Allowed("no volumes")
	}

	targets, err := m.targetStorageClasses(ctx)
	if err != nil {
		pmLogger.Error(err, "targetStorageClasses failed")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	capacity, err := m.requestedCapacity(ctx, pod, targets)
	if err != nil {
		pmLogger.Error(err, "requestedCapacity failed")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	if capacity == 0 {
		return admission.Allowed("no request for TopoLVM")
	}

	ctnr := &pod.Spec.Containers[0]
	quantity := resource.NewQuantity(capacity, resource.DecimalSI)
	if ctnr.Resources.Requests == nil {
		ctnr.Resources.Requests = corev1.ResourceList{}
	}
	ctnr.Resources.Requests[topolvm.CapacityResource] = *quantity
	if ctnr.Resources.Limits == nil {
		ctnr.Resources.Limits = corev1.ResourceList{}
	}
	ctnr.Resources.Limits[topolvm.CapacityResource] = *quantity

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

func (m podMutator) targetStorageClasses(ctx context.Context) (map[string]bool, error) {
	var scl storagev1.StorageClassList
	if err := m.client.List(ctx, &scl); err != nil {
		return nil, err
	}

	targets := make(map[string]bool)
	for _, sc := range scl.Items {
		if sc.Provisioner != topolvm.PluginName {
			continue
		}
		targets[sc.Name] = true
	}
	return targets, nil
}

func (m podMutator) requestedCapacity(ctx context.Context, pod *corev1.Pod, targets map[string]bool) (int64, error) {
	var total int64
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			// CSI volume type does not support direct reference from Pod
			// and may only be referenced in a Pod via a PersistentVolumeClaim
			// https://kubernetes.io/docs/concepts/storage/volumes/#csi
			continue
		}
		pvcName := vol.PersistentVolumeClaim.ClaimName
		name := types.NamespacedName{
			Namespace: pod.Namespace,
			Name:      pvcName,
		}

		var pvc corev1.PersistentVolumeClaim
		if err := m.client.Get(ctx, name, &pvc); err != nil {
			pmLogger.Error(err, "failed to get pvc",
				"pod", pod.Name,
				"namespace", pod.Namespace,
				"pvc", pvcName,
			)
			return 0, err
		}

		if pvc.Spec.StorageClassName == nil {
			// empty class name may appear when DefaultStorageClass admission plugin
			// is turned off, or there are no default StorageClass.
			// https://kubernetes.io/docs/concepts/storage/persistent-volumes/#class-1
			continue
		}
		if !targets[*pvc.Spec.StorageClassName] {
			continue
		}

		// If the Pod has a bound PVC of TopoLVM, the pod will be scheduled
		// to the node of the existing PV.
		if pvc.Status.Phase != corev1.ClaimPending {
			return 0, nil
		}

		var requested int64 = defaultSize
		if req, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
			if req.Value() > defaultSize {
				requested = ((req.Value()-1)>>30 + 1) << 30
			}
		}
		total += requested
	}
	return total, nil
}
