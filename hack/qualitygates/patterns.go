// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"go/ast"
	"go/token"
	"os"
	"strings"
)

// OptimisticLockChecker flags Status().Patch() calls that don't use
// MergeFromWithOptimisticLock. Status conditions can be concurrently
// modified, so optimistic locking is required.
type OptimisticLockChecker struct{}

func (c *OptimisticLockChecker) Name() string { return "optimistic-lock-status-patch" }

func (c *OptimisticLockChecker) CheckFile(fset *token.FileSet, _ *ast.File, path string) []Violation {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	if !strings.Contains(path, "controllers") {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var violations []Violation
	scanner := bufio.NewScanner(f)
	lineNum := 0
	prevLines := make([]string, 0, 5)

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		prevLines = append(prevLines, line)
		if len(prevLines) > 5 {
			prevLines = prevLines[1:]
		}

		if !strings.Contains(line, "Status().Patch(") {
			continue
		}
		// Check the surrounding lines for OptimisticLock usage
		context := strings.Join(prevLines, " ") + line
		if !strings.Contains(context, "OptimisticLock") && !strings.Contains(context, "StrategicMergeFrom") {
			// Check if MergeFrom is used without OptimisticLock
			if strings.Contains(line, "MergeFrom(") || strings.Contains(line, "client.MergeFrom(") {
				violations = append(violations, Violation{
					File:    path,
					Line:    lineNum,
					Rule:    c.Name(),
					Message: "Status().Patch() with MergeFrom lacks OptimisticLock — verify this is intentional (status conditions can be concurrently modified)",
				})
			}
		}
	}
	return violations
}

// ControllerNamingChecker flags controller Name() methods that don't
// follow the dotted hierarchy convention (e.g., "nodeclaim.lifecycle").
// Exempts well-established top-level controllers.
type ControllerNamingChecker struct{}

func (c *ControllerNamingChecker) Name() string { return "controller-dotted-naming" }

var exemptControllerNames = map[string]bool{
	"disruption":     true,
	"provisioner":    true,
	"eviction-queue": true,
}

func (c *ControllerNamingChecker) CheckFile(fset *token.FileSet, file *ast.File, path string) []Violation {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	if !strings.Contains(path, "controllers") {
		return nil
	}

	var violations []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		if fn.Name.Name != "Name" {
			return true
		}
		if fn.Recv == nil || len(fn.Recv.List) == 0 {
			return true
		}
		if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			return true
		}
		retType, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
		if !ok || retType.Name != "string" {
			return true
		}
		if fn.Body == nil || len(fn.Body.List) == 0 {
			return true
		}
		for _, stmt := range fn.Body.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok {
				continue
			}
			for _, r := range ret.Results {
				lit, ok := r.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val := strings.Trim(lit.Value, `"`)
				if !strings.Contains(val, ".") && val != "" && !exemptControllerNames[val] {
					violations = append(violations, Violation{
						File:    path,
						Line:    fset.Position(fn.Pos()).Line,
						Rule:    c.Name(),
						Message: "controller Name() should follow dotted hierarchy (e.g., \"nodeclaim.lifecycle\"), got: " + val,
					})
				}
			}
		}
		return true
	})
	return violations
}

// LogErrorAndReturnChecker flags reconciler methods that both log an error
// and return it. controller-runtime already logs returned errors.
type LogErrorAndReturnChecker struct{}

func (c *LogErrorAndReturnChecker) Name() string { return "no-log-and-return-error" }

func (c *LogErrorAndReturnChecker) CheckFile(fset *token.FileSet, _ *ast.File, path string) []Violation {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	if !strings.Contains(path, "controllers") {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var violations []Violation
	scanner := bufio.NewScanner(f)
	lineNum := 0
	var recentLogError int

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		if strings.Contains(line, ".Error(err") || strings.Contains(line, ".Error(ctx, err") {
			recentLogError = lineNum
		}

		if strings.HasPrefix(line, "return") && strings.Contains(line, "err") && !strings.Contains(line, "nil") {
			if lineNum-recentLogError <= 3 && recentLogError > 0 {
				violations = append(violations, Violation{
					File:    path,
					Line:    lineNum,
					Rule:    c.Name(),
					Message: "error is logged and returned — controller-runtime will double-log; either return the error or log and return nil",
				})
			}
		}
	}
	return violations
}

