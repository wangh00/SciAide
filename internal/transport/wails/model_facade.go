package wails

import (
	"fmt"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/apperr"
)

type ModelFacade struct {
	lifecycle *LifecycleContext
	service   *modelprofile.Service
}

func NewModelFacade(lifecycle *LifecycleContext, service *modelprofile.Service) *ModelFacade {
	return &ModelFacade{lifecycle: lifecycle, service: service}
}
func (f *ModelFacade) SaveModelProfile(request modelprofile.SaveCommand) (modelprofile.Profile, error) {
	return f.service.Save(f.lifecycle.Context(), request)
}
func (f *ModelFacade) ListModelProfiles() ([]modelprofile.Profile, error) {
	return f.service.List(f.lifecycle.Context())
}
func (f *ModelFacade) DeleteModelKey(profileID string) error {
	return f.service.DeleteKey(f.lifecycle.Context(), profileID)
}
func (f *ModelFacade) DeleteModelProfile(profileID string) error {
	return f.service.Delete(f.lifecycle.Context(), profileID)
}
func (f *ModelFacade) TestModelConnection(profileID string) error {
	if err := f.service.Test(f.lifecycle.Context(), profileID); err != nil {
		public := apperr.Public(err)
		if public.Code == "INTERNAL_ERROR" {
			return fmt.Errorf("模型连接测试失败，请检查地址、网络和服务兼容性")
		}
		return fmt.Errorf("%s: %s", public.Code, public.Message)
	}
	return nil
}
