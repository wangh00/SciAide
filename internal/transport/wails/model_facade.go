package wails

import (
	"fmt"

	"github.com/wangh00/SciAide/internal/app/modelprofile"
	"github.com/wangh00/SciAide/internal/app/multimodal"
	"github.com/wangh00/SciAide/internal/app/websearch"
	"github.com/wangh00/SciAide/internal/apperr"
)

type ModelFacade struct {
	lifecycle *LifecycleContext
	service   *modelprofile.Service
	vision    *multimodal.Service
	web       *websearch.Service
}

func NewModelFacade(lifecycle *LifecycleContext, service *modelprofile.Service, vision ...*multimodal.Service) *ModelFacade {
	var fallback *multimodal.Service
	if len(vision) > 0 {
		fallback = vision[0]
	}
	return &ModelFacade{lifecycle: lifecycle, service: service, vision: fallback}
}
func (f *ModelFacade) SaveModelProfile(request modelprofile.SaveCommand) (modelprofile.Profile, error) {
	value, err := f.service.Save(f.lifecycle.Context(), request)
	if err == nil && f.vision != nil {
		f.vision.ForgetProfile(value.ID)
	}
	return value, err
}
func (f *ModelFacade) ListModelProfiles() ([]modelprofile.Profile, error) {
	return f.service.List(f.lifecycle.Context())
}
func (f *ModelFacade) DeleteModelKey(profileID string) error {
	err := f.service.DeleteKey(f.lifecycle.Context(), profileID)
	if err == nil && f.vision != nil {
		f.vision.ForgetProfile(profileID)
	}
	return err
}
func (f *ModelFacade) DeleteModelProfile(profileID string) error {
	err := f.service.Delete(f.lifecycle.Context(), profileID)
	if err == nil && f.vision != nil {
		f.vision.ForgetProfile(profileID)
	}
	return err
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

func (f *ModelFacade) DiscoverModels(request modelprofile.DiscoveryCommand) ([]modelprofile.AvailableModel, error) {
	values, err := f.service.Discover(f.lifecycle.Context(), request)
	if err != nil {
		public := apperr.Public(err)
		if public.Code == "INTERNAL_ERROR" {
			return nil, fmt.Errorf("无法获取模型列表，请检查 Base URL、API Key 或改为手动填写 Model ID")
		}
		return nil, fmt.Errorf("%s: %s", public.Code, public.Message)
	}
	return values, nil
}

func (f *ModelFacade) ListVisionFallbackChannels() ([]multimodal.ChannelView, error) {
	if f.vision == nil {
		return []multimodal.ChannelView{}, nil
	}
	return f.vision.List(f.lifecycle.Context())
}

func (f *ModelFacade) SaveVisionFallbackChannel(request multimodal.SaveCommand) (multimodal.ChannelView, error) {
	if f.vision == nil {
		return multimodal.ChannelView{}, fmt.Errorf("识图兜底服务未配置")
	}
	return f.vision.Save(f.lifecycle.Context(), request)
}

func (f *ModelFacade) DeleteVisionFallbackChannel(channelID string) error {
	if f.vision == nil {
		return fmt.Errorf("识图兜底服务未配置")
	}
	return f.vision.Delete(f.lifecycle.Context(), channelID)
}

func (f *ModelFacade) TestVisionFallbackChannel(channelID string) error {
	if f.vision == nil {
		return fmt.Errorf("识图兜底服务未配置")
	}
	if err := f.vision.Test(f.lifecycle.Context(), channelID); err != nil {
		return fmt.Errorf("识图渠道测试失败：%s", apperr.Public(err).Message)
	}
	return nil
}
