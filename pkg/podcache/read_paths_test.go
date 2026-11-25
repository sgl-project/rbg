/*
Copyright 2026 The RBG Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package podcache

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This inventory flags explicit PodList uses for review. Indirect reads still
// require a manual audit.
func TestProductionPodListInventory(t *testing.T) {
	expected := map[string]int{
		"internal/controller/workloads/rolebasedgroup_controller.go:getPodSchedulingCounts":             1,
		"internal/controller/workloads/rolebasedgroupwarmup_controller.go:getTargetRoleBasedGroupState": 1,
		"internal/controller/workloads/rolebasedgroupwarmup_controller.go:listWarmupPods":               1,
		"pkg/reconciler/sts_reconciler.go:getReplicaStates":                                             1,
		"pkg/reconciler/roleinstance/sync/restart_budget.go:listBudgetPods":                             1,
		"pkg/reconciler/roleinstance/utils/instance_utils.go:GetActiveAndInactivePods":                  1,
		"pkg/reconciler/roleinstanceset/statefulmode/stateful_instance_set_control.go:lifecyclePods":    1,
		"pkg/reconciler/roleinstanceset/statelessmode/sync/update.go:lifecyclePods":                     1,
	}
	got := map[string]int{}
	var ownedPodCallers []string
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				podList := podListReferenced(fn)
				key := filepath.ToSlash(rel) + ":" + fn.Name.Name
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if sel.Sel.Name == "GetActiveAndInactivePods" {
						ownedPodCallers = append(ownedPodCallers, key)
						require.GreaterOrEqual(t, len(call.Args), 3)
						require.True(t, podListSelectorOption(call.Args[2]), "%s must pass a selector", key)
					}
					if podList && sel.Sel.Name == "List" {
						got[key]++
						require.GreaterOrEqual(t, len(call.Args), 3)
						if fn.Name.Name == "GetActiveAndInactivePods" {
							require.Len(t, call.Args, 3)
							forwarded, ok := ast.Unparen(call.Args[2]).(*ast.Ident)
							require.True(t, ok, "%s must forward its list options", key)
							require.Equal(t, "opts", forwarded.Name)
						} else {
							require.True(t, slices.ContainsFunc(call.Args[2:], podListSelectorOption), "%s must pass a label or field selector", key)
						}
					}
					return true
				})
			}
			return nil
		})
		require.NoError(t, err)
	}
	require.Equal(t, expected, got, "update the KEP-284 read-path audit and scoped-read tests before adding a Pod List")
	require.Equal(t, []string{"pkg/reconciler/roleinstance/instance_reconciler.go:getOwnedPods"}, ownedPodCallers)
}

func podListReferenced(body ast.Node) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "PodList" {
			found = true
		}
		return !found
	})
	return found
}

func TestPodListReferenced(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   bool
	}{
		{source: "func f() { var pods corev1.PodList }", want: true},
		{source: "func f() { pods := &corev1.PodList{} }", want: true},
		{source: "func f() { pods := new(corev1.PodList) }", want: true},
		{source: "func f(pods *corev1.PodList) {}", want: true},
		{source: "func f() { pod := &corev1.Pod{} }", want: false},
	} {
		t.Run(tc.source, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "", "package p\n"+tc.source, 0)
			require.NoError(t, err)
			require.Equal(t, tc.want, podListReferenced(file.Decls[0]))
		})
	}
}

func podListSelectorOption(expr ast.Expr) bool {
	switch expr := ast.Unparen(expr).(type) {
	case *ast.UnaryExpr:
		return expr.Op == token.AND && podListSelectorOption(expr.X)
	case *ast.Ident:
		if expr.Obj == nil {
			return false
		}
		assign, ok := expr.Obj.Decl.(*ast.AssignStmt)
		return ok && len(assign.Rhs) == 1 && podListSelectorOption(assign.Rhs[0])
	case *ast.CallExpr:
		option, ok := ast.Unparen(expr.Fun).(*ast.SelectorExpr)
		return ok && len(expr.Args) == 1 && (option.Sel.Name == "MatchingLabels" || option.Sel.Name == "MatchingFields")
	case *ast.CompositeLit:
		typ, ok := expr.Type.(*ast.SelectorExpr)
		if !ok || len(expr.Elts) == 0 {
			return false
		}
		switch typ.Sel.Name {
		case "MatchingLabels", "MatchingLabelsSelector", "MatchingFields", "MatchingFieldsSelector":
			return true
		case "ListOptions":
			return slices.ContainsFunc(expr.Elts, func(elt ast.Expr) bool {
				pair, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					return false
				}
				key, ok := pair.Key.(*ast.Ident)
				return ok && (key.Name == "LabelSelector" || key.Name == "FieldSelector")
			})
		}
	}
	return false
}
