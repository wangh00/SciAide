package wails

import "github.com/wangh00/SciAide/internal/app/chat"

type ChatFacade struct {
	lifecycle *LifecycleContext
	service   *chat.Service
}

func NewChatFacade(lifecycle *LifecycleContext, service *chat.Service) *ChatFacade {
	return &ChatFacade{lifecycle: lifecycle, service: service}
}
func (f *ChatFacade) StartChat(request chat.StartCommand) (chat.Run, error) {
	return f.service.Start(f.lifecycle.Context(), request)
}
func (f *ChatFacade) CancelRun(runID string) error {
	return f.service.Cancel(f.lifecycle.Context(), runID)
}
func (f *ChatFacade) GetRunSnapshot(runID string) (chat.Snapshot, error) {
	return f.service.Snapshot(f.lifecycle.Context(), runID)
}
