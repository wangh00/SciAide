package wails

import (
	"github.com/wangh00/SciAide/internal/app/conversation"
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
func (f *ConversationFacade) ListMessages(conversationID string) ([]conversation.Message, error) {
	return f.service.Messages(f.lifecycle.Context(), conversationID)
}
