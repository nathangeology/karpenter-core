/*
Copyright The Kubernetes Authors.

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

package v1_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"github.com/awslabs/operatorpkg/docs"
	"github.com/awslabs/operatorpkg/wellknown"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/karpenter/pkg/apis"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// annotationKeysFromSource returns the value of every `<Name>AnnotationKey` constant
// declared in labels.go, keyed by constant name. KarpenterAnnotations cannot be checked
// for completeness against the constants at runtime, since Go has no way to enumerate
// them, so the declarations are read from source instead. A constant whose value is not
// of the form `apis.Group + "<suffix>"` fails rather than being skipped, so the check
// cannot pass vacuously. Only labels.go is read, so an annotation key declared in some
// other package stays invisible here; that is the same convention break that left the
// overlay-applied keys undocumented until they moved into this file.
func annotationKeysFromSource() map[string]string {
	file, err := parser.ParseFile(token.NewFileSet(), "labels.go", nil, 0)
	Expect(err).ToNot(HaveOccurred())

	keys := map[string]string{}
	for _, decl := range file.Decls {
		decl, ok := decl.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			continue
		}
		for _, spec := range decl.Specs {
			spec, ok := spec.(*ast.ValueSpec)
			if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
				continue
			}
			name := spec.Names[0].Name
			if !strings.HasSuffix(name, "AnnotationKey") {
				continue
			}
			form := "%s must be declared as apis.Group + \"<suffix>\""
			sum, ok := spec.Values[0].(*ast.BinaryExpr)
			Expect(ok).To(BeTrue(), form, name)
			group, ok := sum.X.(*ast.SelectorExpr)
			Expect(ok).To(BeTrue(), form, name)
			Expect(types.ExprString(group)).To(Equal("apis.Group"), form, name)
			suffix, ok := sum.Y.(*ast.BasicLit)
			Expect(ok).To(BeTrue(), form, name)
			unquoted, err := strconv.Unquote(suffix.Value)
			Expect(err).ToNot(HaveOccurred(), name)
			keys[name] = apis.Group + unquoted
		}
	}
	Expect(keys).ToNot(BeEmpty())
	return keys
}

var _ = Describe("WellKnownAnnotations", func() {
	annotations := v1.KarpenterAnnotations

	It("should document every annotation", func() {
		for _, annotation := range annotations {
			Expect(annotation.Name).ToNot(BeEmpty())
			Expect(annotation.Example).ToNot(BeEmpty(), annotation.Name)
			Expect(annotation.Help).ToNot(BeEmpty(), annotation.Name)
			Expect(annotation.UsedOn).ToNot(BeEmpty(), annotation.Name)
			Expect(annotation.Stage).To(BeElementOf(docs.Alpha, docs.Beta, docs.GA), annotation.Name)
		}
	})
	It("should mark every internal only annotation as alpha", func() {
		for _, annotation := range annotations {
			if annotation.InternalOnly {
				Expect(annotation.Stage).To(Equal(docs.Alpha), annotation.Name)
			}
		}
	})
	It("should not document an annotation twice", func() {
		Expect(lo.FindDuplicatesBy(annotations, func(a wellknown.Annotation) string { return a.Name })).To(BeEmpty())
	})
	It("should document every declared annotation key", func() {
		documented := sets.New(lo.Map(annotations, func(a wellknown.Annotation, _ int) string { return a.Name })...)
		for name, key := range annotationKeysFromSource() {
			Expect(documented.Has(key)).To(BeTrue(),
				"%s (%s) has no wellknown.Annotation in KarpenterAnnotations", name, key)
		}
	})
})
