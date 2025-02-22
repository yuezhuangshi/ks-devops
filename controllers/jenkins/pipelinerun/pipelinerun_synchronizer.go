package pipelinerun

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/jenkins-zh/jenkins-client/pkg/core"
	"github.com/jenkins-zh/jenkins-client/pkg/job"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"kubesphere.io/devops/pkg/api/devops/v1alpha3"
	"kubesphere.io/devops/pkg/kapis/devops/v1alpha3/pipelinerun"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Valid values for event reasons(new reasons could be added in the future)
const (
	PipelineRunSynced     = "PipelineRunSynced"
	FailedPipelineRunSync = "FailedPipelineRunSync"
)

// SyncReconciler is a reconciler for synchronizing PipelineRuns from Jenkins.
// This reconciler is an intermediate reconciler. When we complete the issue
// https://github.com/kubesphere/ks-devops/issues/65, we will remove it.
type SyncReconciler struct {
	client.Client
	log         logr.Logger
	recorder    record.EventRecorder
	JenkinsCore core.JenkinsCore
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *SyncReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.log.WithValues("Pipeline", req.NamespacedName)
	pipeline := &v1alpha3.Pipeline{}
	if err := r.Client.Get(ctx, req.NamespacedName, pipeline); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// get all pipelineruns
	var prList v1alpha3.PipelineRunList
	if err := r.Client.List(ctx, &prList, client.InNamespace(pipeline.Namespace), client.MatchingLabels{
		v1alpha3.PipelineNameLabelKey: pipeline.Name,
	}); err != nil {
		return ctrl.Result{}, err
	}
	prContainer := make(map[string]v1alpha3.PipelineRun)
	for _, pr := range prList.Items {
		runID := pr.Annotations[v1alpha3.JenkinsPipelineRunIDKey]
		prContainer[runID] = pr
	}

	boClient := job.BlueOceanClient{
		JenkinsCore:  r.JenkinsCore,
		Organization: "jenkins",
	}

	jenkinsRuns, err := boClient.GetPipelineRuns(pipeline.Name, pipeline.Namespace)
	if err != nil {
		r.recorder.Eventf(pipeline, v1.EventTypeWarning, FailedPipelineRunSync, "")
		return ctrl.Result{}, err
	}
	var createdPipelineRuns []v1alpha3.PipelineRun
	for _, jenkinsRun := range jenkinsRuns {
		if _, ok := prContainer[jenkinsRun.ID]; !ok {
			pipelineRun, err := r.createPipelineRun(pipeline, &jenkinsRun)
			if err == nil {
				createdPipelineRuns = append(createdPipelineRuns, *pipelineRun)
			} else {
				log.Error(err, "failed to create bare PipelineRun", "JenkinsRun", jenkinsRun)
			}
		}
	}

	if err := r.synchronizedSuccessfully(req.NamespacedName); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("synchronized PipelineRun successfully")

	r.recorder.Eventf(pipeline, v1.EventTypeNormal, PipelineRunSynced,
		"Successfully synchronized %d PipelineRuns(s). PipelineRuns/JenkinsRuns proportion is %d/%d", len(createdPipelineRuns), len(prList.Items), len(jenkinsRuns))
	return ctrl.Result{}, nil
}

func (r *SyncReconciler) synchronizedSuccessfully(key client.ObjectKey) error {
	//pipelineToUpdate := *pipeline.DeepCopy()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		pipelineToUpdate := &v1alpha3.Pipeline{}
		if err := r.Client.Get(context.Background(), key, pipelineToUpdate); err != nil {
			return err
		}
		if _, ok := pipelineToUpdate.Annotations[v1alpha3.PipelineRequestToSyncRunsAnnoKey]; !ok {
			return nil
		}
		// remove the annotation
		delete(pipelineToUpdate.Annotations, v1alpha3.PipelineRequestToSyncRunsAnnoKey)
		// update the Pipeline

		// ignore the conflict
		return r.Client.Update(context.Background(), pipelineToUpdate)
	})
}

func (r *SyncReconciler) createPipelineRun(pipeline *v1alpha3.Pipeline, run *job.PipelineRun) (*v1alpha3.PipelineRun, error) {
	pipelineRun, err := createBarePipelineRun(pipeline, run)
	if err != nil {
		return nil, err
	}
	if err := r.Client.Create(context.Background(), pipelineRun); err != nil {
		return nil, err
	}
	return pipelineRun, nil
}

func createBarePipelineRun(pipeline *v1alpha3.Pipeline, run *job.PipelineRun) (*v1alpha3.PipelineRun, error) {
	branch := ""
	if run.Branch != nil {
		// TODO(johnniang): Refine the branch structure in Jenkins client.
		branch = fmt.Sprint(run.Branch.(map[string]interface{})["url"])
	}
	scm, err := pipelinerun.CreateScm(&pipeline.Spec, branch)
	if err != nil {
		return nil, err
	}
	pr := pipelinerun.CreatePipelineRun(pipeline, nil, scm)
	pr.Annotations[v1alpha3.JenkinsPipelineRunIDKey] = run.ID
	return pr, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("pipeline-synchronizer")
	r.log = ctrl.Log.WithName("pipelinerun-synchronizer")

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha3.Pipeline{}).
		WithEventFilter(predicate.And(predicate.ResourceVersionChangedPredicate{}, requestSyncPredicate())).
		Complete(r)
}

func requestSyncPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			_, ok := e.Meta.GetAnnotations()[v1alpha3.PipelineRequestToSyncRunsAnnoKey]
			return ok
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			_, ok := e.MetaNew.GetAnnotations()[v1alpha3.PipelineRequestToSyncRunsAnnoKey]
			return ok
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
	}
}
