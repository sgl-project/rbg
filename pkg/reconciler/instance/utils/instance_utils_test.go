package utils

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/rbgs/api/workloads/v1alpha1"
)

// TestGetPodComponentID 测试修复后的GetPodComponentID函数
func TestGetPodComponentID(t *testing.T) {
	// 测试用例：覆盖标签存在/不存在、合法值、边界值、超出范围、非数字等场景
	tests := []struct {
		name     string      // 测试用例名称
		pod      *corev1.Pod // 输入的Pod对象
		expected int32       // 预期返回的ComponentID
	}{
		// 场景1：通过标签获取ID（正常情况）
		{
			name: "valid id from label (positive)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "123",
					},
				},
			},
			expected: 123,
		},
		{
			name: "valid id from label (negative)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "-456",
					},
				},
			},
			expected: -456,
		},

		// 场景2：标签存在但值不合法（非数字、超出范围）
		{
			name: "invalid non-numeric id in label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "abc",
					},
				},
			},
			expected: 0, // 错误时返回默认值0
		},
		{
			name: "id in label exceeds int32 max",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "2147483648", // 比MaxInt32大1
					},
				},
			},
			expected: 0,
		},
		{
			name: "id in label less than int32 min",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "-2147483649", // 比MinInt32小1
					},
				},
			},
			expected: 0,
		},

		// 场景3：标签不存在，从Pod名称解析ID（名称格式：前缀-组件名-ID）
		{
			name: "valid id from pod name (positive)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-web-789",  // 最后一段为ID
					Labels: map[string]string{}, // 无ComponentID标签
				},
			},
			expected: 789,
		},
		{
			name: "valid id from pod name (negative)",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-db--987", // 最后一段为负ID
					Labels: map[string]string{},
				},
			},
			expected: -987,
		},

		// 场景4：标签不存在，名称解析不合法
		{
			name: "invalid non-numeric id in pod name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-cache-xyz", // 最后一段非数字
					Labels: map[string]string{},
				},
			},
			expected: 0,
		},
		{
			name: "id in pod name exceeds int32 max",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-store-2147483648",
					Labels: map[string]string{},
				},
			},
			expected: 0,
		},
		{
			name: "id in pod name less than int32 min",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-queue--2147483649",
					Labels: map[string]string{},
				},
			},
			expected: 0,
		},

		// 场景5：边界值测试（int32最大/最小值）
		{
			name: "int32 max value in label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "2147483647", // math.MaxInt32
					},
				},
			},
			expected: math.MaxInt32,
		},
		{
			name: "int32 min value in label",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						v1alpha1.InstanceComponentIDKey: "-2147483648", // math.MinInt32
					},
				},
			},
			expected: math.MinInt32,
		},
		{
			name: "int32 max value in pod name",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "instance-max-2147483647",
					Labels: map[string]string{},
				},
			},
			expected: math.MaxInt32,
		},
	}

	// 执行测试用例
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetPodComponentID(tt.pod)
			assert.Equal(t, tt.expected, result, "测试用例[%s]失败", tt.name)
		})
	}
}
