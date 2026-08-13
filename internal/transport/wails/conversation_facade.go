package wails

import (
	"github.com/wangh00/SciAide/internal/app/conversation"
	"github.com/wangh00/SciAide/internal/modelcap"
)

type ConversationFacade struct {
	lifecycle *LifecycleContext
	service   *conversation.Service
}
type CreateConversationRequest struct {
	ProjectID string `json:"projectId"`
	Title     string `json:"title"`
}

func NewConversationFacade(lifecycle *LifecycleContext, service *conversation.Service) *ConversationFacade {
	return &ConversationFacade{lifecycle: lifecycle, service: service}
}
func (f *ConversationFacade) CreateConversation(request CreateConversationRequest) (conversation.Conversation, error) {
	return f.service.Create(f.lifecycle.Context(), request.ProjectID, request.Title)
}
func (f *ConversationFacade) ListConversations(projectID string) ([]conversation.Conversation, error) {
	return f.service.List(f.lifecycle.Context(), projectID)
}
func (f *ConversationFacade) GetConversation(conversationID string) (conversation.Conversation, error) {
	return f.service.Get(f.lifecycle.Context(), conversationID)
}
func (f *ConversationFacade) ListMessages(conversationID string) ([]conversation.Message, error) {
	return f.service.Messages(f.lifecycle.Context(), conversationID)
}
func (f *ConversationFacade) RemoveConversation(conversationID string) error {
	return f.service.Remove(f.lifecycle.Context(), conversationID)
}

func (f *ConversationFacade) SetPermissionMode(conversationID string, mode conversation.PermissionMode) (conversation.Conversation, error) {
	return f.service.SetPermissionMode(f.lifecycle.Context(), conversationID, mode)
}

func (f *ConversationFacade) SetReasoningLevel(conversationID string, level modelcap.ReasoningLevel) (conversation.Conversation, error) {
	return f.service.SetReasoningLevel(f.lifecycle.Context(), conversationID, level)
}
