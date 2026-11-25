package debugapi

import (
	"fmt"
	"testing"

	"github.com/wangh00/SciAide/internal/apperr"
)

// Plain workflow/runtime errors carry the exact text the production Wails
// transport renders to users; the debug client must see the same message
// instead of the generic INTERNAL_ERROR mask.
func TestPublicErrorKeepsPlainErrorText(t *testing.T) {
	plain := fmt.Errorf("AI 证据审核判断当前选择不足以覆盖原研究范围，或遗漏了推荐摘录；请补充选择，或明确勾选“以有限证据继续”后生成有边界的初稿")
	public := publicError(plain)
	if public.Code != "DEBUG_OPERATION_FAILED" {
		t.Fatalf("plain error code: got %q, want DEBUG_OPERATION_FAILED", public.Code)
	}
	if public.Message != plain.Error() {
		t.Fatalf("plain error message: got %q, want the original text", public.Message)
	}
	wrapped := publicError(fmt.Errorf("最新 AI 回答的引用快照无效：%w", plain))
	if wrapped.Code != "DEBUG_OPERATION_FAILED" || wrapped.Message == "" || wrapped.Message == "SciAide 遇到内部错误，请使用错误编号查看诊断信息。" {
		t.Fatalf("wrapped plain error lost its text: %+v", wrapped)
	}
}

// Typed debug errors keep their public projection, including wrapped ones.
func TestPublicErrorPreservesTypedAppErrors(t *testing.T) {
	typed := &apperr.Error{Code: "DEBUG_REQUEST_INVALID", UserMessage: "run does not belong to this debug case"}
	public := publicError(typed)
	if public.Code != "DEBUG_REQUEST_INVALID" || public.Message != "run does not belong to this debug case" {
		t.Fatalf("typed error projection changed: %+v", public)
	}
	nested := fmt.Errorf("prepare case: %w", &apperr.Error{Code: "DEBUG_MODEL_DISABLED", UserMessage: "调试实例未启用模型调用；需显式启用，可能产生费用。"})
	public = publicError(nested)
	if public.Code != "DEBUG_MODEL_DISABLED" {
		t.Fatalf("wrapped typed error code: got %q, want DEBUG_MODEL_DISABLED", public.Code)
	}
}
