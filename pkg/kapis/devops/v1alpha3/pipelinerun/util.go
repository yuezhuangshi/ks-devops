package pipelinerun

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"kubesphere.io/devops/pkg/api/devops/v1alpha3"
	"kubesphere.io/devops/pkg/apiserver/query"
	"kubesphere.io/devops/pkg/client/devops"
)

func buildLabelSelector(queryParam *query.Query, pipelineName, branchName string) (labels.Selector, error) {
	labelSelector := queryParam.Selector()
	rq, err := labels.NewRequirement(v1alpha3.PipelineNameLabelKey, selection.Equals, []string{pipelineName})
	if err != nil {
		// should never happen
		return nil, err
	}
	labelSelector = labelSelector.Add(*rq)
	if branchName != "" {
		rq, err = labels.NewRequirement(v1alpha3.SCMRefNameLabelKey, selection.Equals, []string{branchName})
		if err != nil {
			// should never happen
			return nil, err
		}
		labelSelector = labelSelector.Add(*rq)
	}
	return labelSelector, nil
}

func convertPipelineRunsToObject(prs []v1alpha3.PipelineRun) []runtime.Object {
	var result []runtime.Object
	for i := range prs {
		result = append(result, &prs[i])
	}
	return result
}

func convertParameters(payload *devops.RunPayload) []v1alpha3.Parameter {
	if payload == nil {
		return nil
	}
	var parameters []v1alpha3.Parameter
	for _, parameter := range payload.Parameters {
		if parameter.Name == "" || parameter.Value == "" {
			continue
		}
		parameters = append(parameters, v1alpha3.Parameter{
			Name:  parameter.Name,
			Value: fmt.Sprint(parameter.Value),
		})
	}
	return parameters
}

// CreateScm creates SCM for multi-branch Pipeline.
func CreateScm(ps *v1alpha3.PipelineSpec, branch string) (*v1alpha3.SCM, error) {
	var scm *v1alpha3.SCM
	if ps.Type == v1alpha3.MultiBranchPipelineType {
		if branch == "" {
			return nil, errors.New("missing branch name for running a multi-branch Pipeline")
		}
		// TODO validate if the branch dose exist
		// we can not determine what is reference type here. So we set reference name only for now
		scm = &v1alpha3.SCM{
			RefName: branch,
			RefType: "",
		}
	}
	return scm, nil
}

func getPipelineRef(pipeline *v1alpha3.Pipeline) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		Kind:      pipeline.Kind,
		Name:      pipeline.GetName(),
		Namespace: pipeline.GetNamespace(),
	}
}

// CreatePipelineRun creates a bare PipelineRun.
func CreatePipelineRun(pipeline *v1alpha3.Pipeline, payload *devops.RunPayload, scm *v1alpha3.SCM) *v1alpha3.PipelineRun {
	controllerRef := metav1.NewControllerRef(pipeline, pipeline.GroupVersionKind())
	return &v1alpha3.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			// the name should be like "pipeline-xyzmnt", so we set generate name "pipeline-" here.
			GenerateName:    pipeline.GetName() + "-",
			Namespace:       pipeline.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{*controllerRef},
			Annotations:     map[string]string{},
		},
		Spec: v1alpha3.PipelineRunSpec{
			PipelineRef:  getPipelineRef(pipeline),
			PipelineSpec: &pipeline.Spec,
			Parameters:   convertParameters(payload),
			SCM:          scm,
		},
	}
}
