package chat

import (
	"fmt"
	"unicode/utf8"
)

// SQLite length(TEXT) and the chat entry point count Unicode code points,
// not UTF-8 bytes. Keep the storage contract shared across both layers.
const MaxWorkflowPromptChars = 262_144

func ValidateWorkflowPrompt(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("科研阶段输入不是有效 UTF-8 文本；内容未保存")
	}
	count := utf8.RuneCountInString(text)
	if count == 0 {
		return fmt.Errorf("科研阶段输入不能为空")
	}
	if count > MaxWorkflowPromptChars {
		return fmt.Errorf("科研阶段输入超过 %d 字符的存储上限（实际 %d 字符，%d 字节）；请减少重复结果或拆分阶段输入，内容未截断", MaxWorkflowPromptChars, count, len(text))
	}
	return nil
}
