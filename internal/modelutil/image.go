package modelutil

import (
	"errors"
	"strings"

	"github.com/wangh00/SciAide/internal/apperr"
)

// IsImageInputUnsupported only accepts explicit request-shape rejections.
// Transient failures and errors about one malformed/oversized image must not
// poison the runtime capability cache for the selected model.
func IsImageInputUnsupported(err error) bool {
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Retryable || appErr.HTTPStatus != 400 && appErr.HTTPStatus != 422 {
		return false
	}
	detail := strings.ToLower(strings.Join([]string{appErr.UserMessage, appErr.Details, appErr.Error()}, " "))
	mentionsImage := containsAny(detail, "image_url", "input_image", "image input", "image content", "images are not", "vision input", "multimodal input", "图片输入", "图片内容", "图像输入")
	if !mentionsImage {
		return false
	}
	if containsAny(detail, "invalid image", "malformed image", "image too large", "image size", "image dimensions", "invalid base64", "unsupported image format", "图片格式", "图片过大", "图像格式") {
		return false
	}
	return containsAny(detail,
		"only supports text", "supports text input only", "text-only model", "text only model",
		"does not support image", "doesn't support image", "images are not supported", "image input is not supported",
		"unsupported content type 'image", "unsupported content type \"image", "unsupported content type: image",
		"不支持图片", "不支持图像", "仅支持文本", "只支持文本",
	)
}

func containsAny(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
