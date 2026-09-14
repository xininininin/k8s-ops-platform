package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/example/k8s-ops-platform/api/v1alpha1"
	"github.com/example/k8s-ops-platform/internal/diagnosis"
)

type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

type Config struct {
	AutoPodRestart        bool
	AutoDeploymentRestart bool
	ActionTimeoutSeconds  int32
	ExcludedNamespaces    []string
}

func RiskFor(action api.ActionType) Risk {
	switch action {
	case api.ActionTypePodRestart:
		return RiskLow
	case api.ActionTypeDeploymentRestart:
		return RiskMedium
	case api.ActionTypeNodeMaintenance:
		return RiskHigh
	default:
		return RiskHigh
	}
}

func (c Config) ShouldAutoCreate(action api.ActionType) bool {
	switch action {
	case api.ActionTypePodRestart:
		return c.AutoPodRestart
	case api.ActionTypeDeploymentRestart:
		return c.AutoDeploymentRestart
	default:
		return false
	}
}

type Sink struct {
	Client client.Client
	Config Config
	Logger *slog.Logger
}

func (s *Sink) Handle(result diagnosis.DiagnosisResult) {
	for _, namespace := range s.Config.ExcludedNamespaces {
		if result.Namespace == namespace {
			return
		}
	}
	if result.SuggestedAction == "" {
		return
	}
	actionType := api.ActionType(result.SuggestedAction)
	risk := RiskFor(actionType)
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("recommended_action", "resource_kind", result.ResourceKind, "namespace", result.Namespace,
		"name", result.Name, "action_type", actionType, "risk", risk, "auto_create", s.Config.ShouldAutoCreate(actionType))
	if !s.Config.ShouldAutoCreate(actionType) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.createIfNoActive(ctx, result, actionType); err != nil {
		logger.Error("create action from policy", "error", err, "action_type", actionType, "namespace", result.Namespace, "name", result.Name)
	}
}

func (s *Sink) createIfNoActive(ctx context.Context, result diagnosis.DiagnosisResult, actionType api.ActionType) error {
	timeout := s.Config.ActionTimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}
	namespace := result.Namespace
	if namespace == "" {
		namespace = "default"
	}
	action := &api.Action{
		ObjectMeta: metav1.ObjectMeta{Name: "diagnosis-" + strings.ToLower(string(actionType)) + "-" + targetHash(result, actionType), Namespace: namespace, Labels: map[string]string{"ops.example.io/target-hash": targetHash(result, actionType)}},
		Spec:       api.ActionSpec{ActionType: actionType, Target: api.TargetReference{Kind: result.ResourceKind, Namespace: result.Namespace, Name: result.Name}, Reason: result.Reason, TimeoutSeconds: timeout, RequestedBy: "diagnosis-agent", TriggerSource: "DiagnosisAgent"},
	}
	if err := s.Client.Create(ctx, action); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create Action: %w", err)
	}
	return nil
}

func targetHash(result diagnosis.DiagnosisResult, actionType api.ActionType) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{result.ResourceKind, result.Namespace, result.Name, result.Reason, string(actionType)}, "\x00")))
	return hex.EncodeToString(sum[:8])
}
