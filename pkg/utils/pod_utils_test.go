package utils

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodRunningAndReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      corev1.Pod
		expected bool
	}{
		{
			name: "pod running and ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod running but not ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod pending and ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod succeeded and ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodSucceeded,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod running with no ready condition",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase:      corev1.PodRunning,
					Conditions: []corev1.PodCondition{},
				},
			},
			expected: false,
		},
		{
			name: "pod with unknown phase and ready condition",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodUnknown,
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := PodRunningAndReady(tt.pod)
				if result != tt.expected {
					t.Errorf("PodRunningAndReady() = %v, want %v", result, tt.expected)
				}
			},
		)
	}
}

func Test_podReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      corev1.Pod
		expected bool
	}{
		{
			name: "pod ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "pod not ready",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pod with no ready condition",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := podReady(tt.pod)
				if result != tt.expected {
					t.Errorf("podReady() = %v, want %v", result, tt.expected)
				}
			},
		)
	}
}

func Test_podReadyConditionTrue(t *testing.T) {
	tests := []struct {
		name     string
		status   corev1.PodStatus
		expected bool
	}{
		{
			name: "ready condition true",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
			expected: true,
		},
		{
			name: "ready condition false",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionFalse,
					},
				},
			},
			expected: false,
		},
		{
			name: "ready condition unknown",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionUnknown,
					},
				},
			},
			expected: false,
		},
		{
			name: "no ready condition",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{},
			},
			expected: false,
		},
		{
			name:     "nil conditions",
			status:   corev1.PodStatus{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := podReadyConditionTrue(tt.status)
				if result != tt.expected {
					t.Errorf("podReadyConditionTrue() = %v, want %v", result, tt.expected)
				}
			},
		)
	}
}

func Test_getPodReadyCondition(t *testing.T) {
	tests := []struct {
		name     string
		status   corev1.PodStatus
		expected *corev1.PodCondition
	}{
		{
			name: "has ready condition",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				},
			},
			expected: &corev1.PodCondition{
				Type:   corev1.PodReady,
				Status: corev1.ConditionTrue,
			},
		},
		{
			name: "no ready condition",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodInitialized,
						Status: corev1.ConditionTrue,
					},
				},
			},
			expected: nil,
		},
		{
			name: "empty conditions",
			status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{},
			},
			expected: nil,
		},
		{
			name:     "nil conditions",
			status:   corev1.PodStatus{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := getPodReadyCondition(tt.status)
				if tt.expected == nil {
					if result != nil {
						t.Errorf("getPodReadyCondition() = %v, want nil", result)
					}
				} else {
					if result == nil {
						t.Errorf("getPodReadyCondition() = nil, want %v", tt.expected)
					} else if result.Type != tt.expected.Type || result.Status != tt.expected.Status {
						t.Errorf("getPodReadyCondition() = %v, want %v", result, tt.expected)
					}
				}
			},
		)
	}
}

func Test_getPodCondition(t *testing.T) {
	readyCondition := corev1.PodCondition{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	}

	scheduledCondition := corev1.PodCondition{
		Type:   corev1.PodScheduled,
		Status: corev1.ConditionTrue,
	}

	tests := []struct {
		name           string
		status         *corev1.PodStatus
		conditionType  corev1.PodConditionType
		expectedIndex  int
		expectedResult *corev1.PodCondition
	}{
		{
			name: "find existing condition",
			status: &corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					scheduledCondition,
					readyCondition,
				},
			},
			conditionType:  corev1.PodReady,
			expectedIndex:  1,
			expectedResult: &readyCondition,
		},
		{
			name: "condition not found",
			status: &corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					scheduledCondition,
				},
			},
			conditionType:  corev1.PodReady,
			expectedIndex:  -1,
			expectedResult: nil,
		},
		{
			name:           "nil status",
			status:         nil,
			conditionType:  corev1.PodReady,
			expectedIndex:  -1,
			expectedResult: nil,
		},
		{
			name: "empty conditions",
			status: &corev1.PodStatus{
				Conditions: []corev1.PodCondition{},
			},
			conditionType:  corev1.PodReady,
			expectedIndex:  -1,
			expectedResult: nil,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				index, result := getPodCondition(tt.status, tt.conditionType)

				if index != tt.expectedIndex {
					t.Errorf("getPodCondition() index = %v, want %v", index, tt.expectedIndex)
				}

				if tt.expectedResult == nil {
					if result != nil {
						t.Errorf("getPodCondition() result = %v, want nil", result)
					}
				} else {
					if result == nil {
						t.Errorf("getPodCondition() result = nil, want %v", tt.expectedResult)
					} else if result.Type != tt.expectedResult.Type || result.Status != tt.expectedResult.Status {
						t.Errorf("getPodCondition() result = %v, want %v", result, tt.expectedResult)
					}
				}
			},
		)
	}
}

func Test_getPodConditionFromList(t *testing.T) {
	readyCondition := corev1.PodCondition{
		Type:   corev1.PodReady,
		Status: corev1.ConditionTrue,
	}

	scheduledCondition := corev1.PodCondition{
		Type:   corev1.PodScheduled,
		Status: corev1.ConditionTrue,
	}

	tests := []struct {
		name           string
		conditions     []corev1.PodCondition
		conditionType  corev1.PodConditionType
		expectedIndex  int
		expectedResult *corev1.PodCondition
	}{
		{
			name: "find existing condition",
			conditions: []corev1.PodCondition{
				scheduledCondition,
				readyCondition,
			},
			conditionType:  corev1.PodReady,
			expectedIndex:  1,
			expectedResult: &readyCondition,
		},
		{
			name: "condition not found",
			conditions: []corev1.PodCondition{
				scheduledCondition,
			},
			conditionType:  corev1.PodReady,
			expectedIndex:  -1,
			expectedResult: nil,
		},
		{
			name:           "nil conditions",
			conditions:     nil,
			conditionType:  corev1.PodReady,
			expectedIndex:  -1,
			expectedResult: nil,
		},
		{
			name:           "empty conditions",
			conditions:     []corev1.PodCondition{},
			conditionType:  corev1.PodReady,
			expectedIndex:  -1,
			expectedResult: nil,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				index, result := getPodConditionFromList(tt.conditions, tt.conditionType)

				if index != tt.expectedIndex {
					t.Errorf("getPodConditionFromList() index = %v, want %v", index, tt.expectedIndex)
				}

				if tt.expectedResult == nil {
					if result != nil {
						t.Errorf("getPodConditionFromList() result = %v, want nil", result)
					}
				} else {
					if result == nil {
						t.Errorf("getPodConditionFromList() result = nil, want %v", tt.expectedResult)
					} else if result.Type != tt.expectedResult.Type || result.Status != tt.expectedResult.Status {
						t.Errorf("getPodConditionFromList() result = %v, want %v", result, tt.expectedResult)
					}
				}
			},
		)
	}
}

func TestContainerRestarted(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "running pod with restarted init container",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					InitContainerStatuses: []corev1.ContainerStatus{
						{
							Name:         "init-container",
							RestartCount: 1,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "running pod with restarted regular container",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:         "regular-container",
							RestartCount: 1,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "running pod with patio-runtime restarted (should be ignored)",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:         "patio-runtime",
							RestartCount: 1,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "running pod with no restarts",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:         "regular-container",
							RestartCount: 0,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "pending pod with restarts",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:         "regular-container",
							RestartCount: 1,
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "succeeded pod with restarts",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodSucceeded,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:         "regular-container",
							RestartCount: 1,
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "failed pod with restarts",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodFailed,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:         "regular-container",
							RestartCount: 1,
						},
					},
				},
			},
			expected: false,
		},
		{
			name:     "nil pod",
			pod:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := ContainerRestarted(tt.pod)
				if result != tt.expected {
					t.Errorf("ContainerRestarted() = %v, want %v", result, tt.expected)
				}
			},
		)
	}
}

func TestPodDeleted(t *testing.T) {
	now := metav1.Now()

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "pod with deletion timestamp",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &now,
				},
			},
			expected: true,
		},
		{
			name: "pod without deletion timestamp",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: nil,
				},
			},
			expected: false,
		},
		{
			name:     "nil pod",
			pod:      nil,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				result := PodDeleted(tt.pod)
				if result != tt.expected {
					t.Errorf("PodDeleted() = %v, want %v", result, tt.expected)
				}
			},
		)
	}
}
