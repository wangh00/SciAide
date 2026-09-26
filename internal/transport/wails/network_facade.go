package wails

import (
	"fmt"
	"github.com/wangh00/SciAide/internal/network"
)

func (f *ModelFacade) GetNetworkConfig() (network.Config, error) {
	s := network.Current()
	if s == nil {
		return network.Config{}, fmt.Errorf("网络设置不可用")
	}
	return s.Get(), nil
}
func (f *ModelFacade) SaveNetworkConfig(c network.Config) error {
	s := network.Current()
	if s == nil {
		return fmt.Errorf("网络设置不可用")
	}
	return s.Save(f.lifecycle.Context(), c)
}
