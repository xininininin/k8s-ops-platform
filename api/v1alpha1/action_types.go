package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type ActionType string

const (
	ActionTypePodRestart        ActionType = "PodRestart"
	ActionTypeDeploymentRestart ActionType = "DeploymentRestart"
	ActionTypeNodeMaintenance   ActionType = "NodeMaintenance"
)

type ActionPhase string

const (
	ActionPhasePending   ActionPhase = "Pending"
	ActionPhaseRunning   ActionPhase = "Running"
	ActionPhaseVerifying ActionPhase = "Verifying"
	ActionPhaseSucceeded ActionPhase = "Succeeded"
	ActionPhaseFailed    ActionPhase = "Failed"
)

type TargetReference struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

type ActionSpec struct {
	ActionType     ActionType      `json:"actionType"`
	Target         TargetReference `json:"target"`
	Reason         string          `json:"reason"`
	TimeoutSeconds int32           `json:"timeoutSeconds,omitempty"`
	RequestedBy    string          `json:"requestedBy"`
	TriggerSource  string          `json:"triggerSource"`
}

type ActionStatus struct {
	Phase              ActionPhase        `json:"phase,omitempty"`
	Message            string             `json:"message,omitempty"`
	StartedAt          *metav1.Time       `json:"startedAt,omitempty"`
	CompletedAt        *metav1.Time       `json:"completedAt,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	RetryCount         int32              `json:"retryCount,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ExecutionState     string             `json:"executionState,omitempty"`
	TargetUID          string             `json:"targetUID,omitempty"`
	WorkloadKind       string             `json:"workloadKind,omitempty"`
	WorkloadName       string             `json:"workloadName,omitempty"`
}

func (s ActionStatus) DeepCopyValue() ActionStatus {
	out := s
	s.DeepCopyInto(&out)
	return out
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=opa
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.actionType`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Action struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ActionSpec   `json:"spec,omitempty"`
	Status            ActionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ActionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Action `json:"items"`
}

func init() { SchemeBuilder.Register(&Action{}, &ActionList{}) }
