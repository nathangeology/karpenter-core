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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Violation struct {
	File    string
	Line    int
	Rule    string
	Message string
}

type Checker interface {
	Name() string
	CheckFile(fset *token.FileSet, file *ast.File, path string) []Violation
}

func main() {
	root := "."
	diffOnly := false
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--diff-only":
			diffOnly = true
		case "--help", "-h":
			fmt.Println("Usage: qualitygates [--diff-only] [path]")
			fmt.Println()
			fmt.Println("Runs Karpenter-specific quality gates based on reviewer analysis.")
			fmt.Println("Checks enforce conventions that maintainers (jmdeal, DerekFrank,")
			fmt.Println("jonathan-innis, njtran, tallaxes) consistently require in reviews.")
			fmt.Println()
			fmt.Println("Flags:")
			fmt.Println("  --diff-only  Only check files changed since last commit")
			fmt.Println()
			fmt.Println("Checks:")
			fmt.Println("  error-requeue-antipattern   Never return error AND RequeueAfter together")
			fmt.Println("  no-lo-foreach               Use standard for loops, not lo.ForEach")
			fmt.Println("  no-time-now                 Use injected clock interface, not time.Now()")
			fmt.Println("  no-printf-in-tests          Use Gomega assertions, not fmt.Printf in tests")
			fmt.Println("  error-wrapping-format       Use comma-space: fmt.Errorf(\"ctx, %w\", err)")
			fmt.Println("  map-requires-cleanup        Maps in state must have delete/cleanup logic")
			fmt.Println("  kubeclient-naming           Client field must be named \"kubeClient\"")
			fmt.Println("  optimistic-lock-status-patch Status patches should use OptimisticLock")
			fmt.Println("  controller-dotted-naming    Controller names use dotted hierarchy")
			fmt.Println("  no-log-and-return-error     Don't log.Error() and return err (double-logs)")
			os.Exit(0)
		default:
			root = arg
		}
	}

	checkers := []Checker{
		&ErrorRequeueChecker{},
		&LoForEachChecker{},
		&TimeNowChecker{},
		&PrintfInTestChecker{},
		&ErrorWrappingChecker{},
		&MapCleanupChecker{},
		&KubeClientNamingChecker{},
		&OptimisticLockChecker{},
		&ControllerNamingChecker{},
		&LogErrorAndReturnChecker{},
	}

	var changedFiles map[string]bool
	if diffOnly {
		changedFiles = getChangedFiles()
		if len(changedFiles) == 0 {
			fmt.Println("✓ No changed Go files to check")
			os.Exit(0)
		}
	}

	var violations []Violation
	fset := token.NewFileSet()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "vendor" || base == ".git" || base == "tools" || base == "hack" || base == "designs" || base == "charts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if diffOnly && !changedFiles[path] {
			return nil
		}
		f, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return nil
		}
		for _, checker := range checkers {
			violations = append(violations, checker.CheckFile(fset, f, path)...)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error walking directory: %v\n", err)
		os.Exit(1)
	}

	if len(violations) == 0 {
		fmt.Println("✓ All quality gates passed")
		os.Exit(0)
	}

	grouped := map[string][]Violation{}
	for _, v := range violations {
		grouped[v.Rule] = append(grouped[v.Rule], v)
	}

	fmt.Printf("Found %d quality gate violations:\n\n", len(violations))
	for rule, vs := range grouped {
		fmt.Printf("━━ %s (%d) ━━\n", rule, len(vs))
		for _, v := range vs {
			fmt.Printf("  %s:%d: %s\n", v.File, v.Line, v.Message)
		}
		fmt.Println()
	}
	os.Exit(1)
}

// ErrorRequeueChecker flags returning both a non-nil error and a RequeueAfter result.
// controller-runtime will log warnings when both are returned.
type ErrorRequeueChecker struct{}

func (c *ErrorRequeueChecker) Name() string { return "error-requeue-antipattern" }

func (c *ErrorRequeueChecker) CheckFile(fset *token.FileSet, file *ast.File, path string) []Violation {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	if !strings.Contains(path, "controllers") {
		return nil
	}

	var violations []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 2 {
			return true
		}
		firstResult := nodeString(ret.Results[0])
		secondResult := nodeString(ret.Results[1])
		if strings.Contains(firstResult, "RequeueAfter") && secondResult != "nil" && !strings.Contains(secondResult, "nil") {
			violations = append(violations, Violation{
				File:    path,
				Line:    fset.Position(ret.Pos()).Line,
				Rule:    c.Name(),
				Message: "returning error alongside RequeueAfter — controller-runtime will log warnings; return one or the other",
			})
		}
		return true
	})
	return violations
}

// LoForEachChecker bans lo.ForEach usage — standard for loops are preferred.
type LoForEachChecker struct{}

func (c *LoForEachChecker) Name() string { return "no-lo-foreach" }

func (c *LoForEachChecker) CheckFile(fset *token.FileSet, file *ast.File, path string) []Violation {
	var violations []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "lo" && sel.Sel.Name == "ForEach" {
			violations = append(violations, Violation{
				File:    path,
				Line:    fset.Position(call.Pos()).Line,
				Rule:    c.Name(),
				Message: "use a standard for loop instead of lo.ForEach",
			})
		}
		return true
	})
	return violations
}

// TimeNowChecker flags direct time.Now() calls in non-test production code.
// The project uses an injected clock interface.
type TimeNowChecker struct{}

func (c *TimeNowChecker) Name() string { return "no-time-now" }

func (c *TimeNowChecker) CheckFile(fset *token.FileSet, file *ast.File, path string) []Violation {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	// Allow in test utilities and metrics constants (benchmarking helpers)
	if strings.Contains(path, "pkg/test/") || strings.Contains(path, "pkg/metrics/constants") {
		return nil
	}

	var violations []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "time" && sel.Sel.Name == "Now" {
			violations = append(violations, Violation{
				File:    path,
				Line:    fset.Position(call.Pos()).Line,
				Rule:    c.Name(),
				Message: "use the injected clock interface instead of time.Now()",
			})
		}
		return true
	})
	return violations
}

// PrintfInTestChecker flags fmt.Printf/fmt.Println in test files.
// Tests should use Gomega assertions, not print statements.
type PrintfInTestChecker struct{}

func (c *PrintfInTestChecker) Name() string { return "no-printf-in-tests" }

func (c *PrintfInTestChecker) CheckFile(fset *token.FileSet, file *ast.File, path string) []Violation {
	if !strings.HasSuffix(path, "_test.go") {
		return nil
	}
	// Allow in benchmark tests which legitimately report results
	if strings.Contains(path, "benchmark") {
		return nil
	}

	var violations []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "fmt" && (sel.Sel.Name == "Printf" || sel.Sel.Name == "Println") {
			violations = append(violations, Violation{
				File:    path,
				Line:    fset.Position(call.Pos()).Line,
				Rule:    c.Name(),
				Message: "use Gomega assertions instead of fmt.Printf/Println in tests",
			})
		}
		return true
	})
	return violations
}

// ErrorWrappingChecker flags error wrapping that uses ": %w" instead of ", %w".
// The Karpenter convention is: fmt.Errorf("context, %w", err)
type ErrorWrappingChecker struct{}

func (c *ErrorWrappingChecker) Name() string { return "error-wrapping-format" }

var errfColonPattern = regexp.MustCompile(`:\s*%w`)
var errfCommaPattern = regexp.MustCompile(`,\s*%w`)

func (c *ErrorWrappingChecker) CheckFile(fset *token.FileSet, _ *ast.File, path string) []Violation {
	if strings.HasSuffix(path, "_test.go") {
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
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if !strings.Contains(line, "fmt.Errorf") {
			continue
		}
		if !strings.Contains(line, "%w") {
			continue
		}
		if errfColonPattern.MatchString(line) && !errfCommaPattern.MatchString(line) {
			violations = append(violations, Violation{
				File:    path,
				Line:    lineNum,
				Rule:    c.Name(),
				Message: "use comma-space format: fmt.Errorf(\"context, %w\", err) not colon",
			})
		}
	}
	return violations
}

// MapCleanupChecker flags map fields in structs under pkg/controllers/state/
// that don't have corresponding delete/cleanup logic nearby.
// This is a heuristic check — it looks for map declarations without
// a corresponding delete() call in the same file.
type MapCleanupChecker struct{}

func (c *MapCleanupChecker) Name() string { return "map-requires-cleanup" }

func (c *MapCleanupChecker) CheckFile(fset *token.FileSet, file *ast.File, path string) []Violation {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	if !strings.Contains(path, "controllers/state") {
		return nil
	}

	type mapField struct {
		name string
		line int
	}
	var mapFields []mapField

	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if _, ok := field.Type.(*ast.MapType); ok {
				for _, name := range field.Names {
					mapFields = append(mapFields, mapField{
						name: name.Name,
						line: fset.Position(field.Pos()).Line,
					})
				}
			}
		}
		return true
	})

	if len(mapFields) == 0 {
		return nil
	}

	// Read file content to check for delete() calls
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	fileStr := string(content)

	var violations []Violation
	for _, mf := range mapFields {
		if !strings.Contains(fileStr, "delete(") || !strings.Contains(fileStr, mf.name) {
			hasDelete := false
			deletePattern := fmt.Sprintf("delete(%s", mf.name)
			deletePattern2 := fmt.Sprintf("delete(c.%s", mf.name)
			if strings.Contains(fileStr, deletePattern) || strings.Contains(fileStr, deletePattern2) {
				hasDelete = true
			}
			if !hasDelete && !strings.Contains(fileStr, mf.name+"=") && !strings.Contains(fileStr, mf.name+" =") {
				// Skip fields that are only declared but never assigned outside the struct literal
				continue
			}
			if !hasDelete {
				violations = append(violations, Violation{
					File:    path,
					Line:    mf.line,
					Rule:    c.Name(),
					Message: fmt.Sprintf("map field %q has no corresponding delete() call — may leak entries", mf.name),
				})
			}
		}
	}
	return violations
}

// KubeClientNamingChecker flags Kubernetes client fields not named "kubeClient".
type KubeClientNamingChecker struct{}

func (c *KubeClientNamingChecker) Name() string { return "kubeclient-naming" }

func (c *KubeClientNamingChecker) CheckFile(fset *token.FileSet, file *ast.File, path string) []Violation {
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}
	if !strings.Contains(path, "controllers") {
		return nil
	}

	var violations []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			fieldType := nodeString(field.Type)
			if !strings.Contains(fieldType, "client.Client") {
				continue
			}
			for _, name := range field.Names {
				if name.Name != "kubeClient" && name.Name != "KubeClient" {
					violations = append(violations, Violation{
						File:    path,
						Line:    fset.Position(field.Pos()).Line,
						Rule:    c.Name(),
						Message: fmt.Sprintf("client.Client field should be named \"kubeClient\", not %q", name.Name),
					})
				}
			}
		}
		return true
	})
	return violations
}

func getChangedFiles() map[string]bool {
	cmd := exec.Command("git", "diff", "--name-only", "--diff-filter=ACM", "HEAD~1")
	out, err := cmd.Output()
	if err != nil {
		cmd = exec.Command("git", "diff", "--name-only", "--cached")
		out, _ = cmd.Output()
	}
	files := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasSuffix(line, ".go") {
			files[line] = true
		}
	}
	return files
}

func nodeString(n ast.Node) string {
	switch v := n.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return nodeString(v.X) + "." + v.Sel.Name
	case *ast.CompositeLit:
		return nodeString(v.Type) + "{...}"
	case *ast.UnaryExpr:
		return nodeString(v.X)
	case *ast.CallExpr:
		return nodeString(v.Fun) + "(...)"
	case *ast.BasicLit:
		return v.Value
	default:
		return "?"
	}
}
