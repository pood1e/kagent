// Package compilation assembles the native Harness compilers for reconciliation
// and API admission.
package compilation

import (
	"istio.io/istio/pkg/kube/krt"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/internal/translator"
	"github.com/kagent-dev/kagent/go/core/internal/translator/byo"
	"github.com/kagent-dev/kagent/go/core/internal/translator/claude"
	"github.com/kagent-dev/kagent/go/core/internal/translator/codex"
	"github.com/kagent-dev/kagent/go/core/internal/translator/kagent"
)

// New builds the same compiler set for reactive and one-time compilation.
func New(ctx krt.HandlerContext, collections translator.Collections, settings *configv1.RuntimeDefaultsConfig, images *configv1.HarnessImagesConfig) *translator.Compiler {
	return translator.NewCompiler(ctx, collections, map[translator.HarnessType]translator.HarnessCompiler{
		translator.HarnessTypeKagent: kagent.NewCompiler(ctx, collections, settings),
		translator.HarnessTypeCodex:  codex.NewCompiler(ctx, collections, settings),
		translator.HarnessTypeClaude: claude.NewCompiler(ctx, collections, settings),
		translator.HarnessTypeBYO:    byo.NewCompiler(ctx, collections, settings),
	}, images, settings)
}
