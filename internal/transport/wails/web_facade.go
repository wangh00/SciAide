package wails

import "github.com/wangh00/SciAide/internal/app/websearch"

func (f *ModelFacade) SetWebSearch(s *websearch.Service) { f.web = s }
func (f *ModelFacade) ListSearchChannels() ([]websearch.Channel, error) {
	return f.web.List(f.lifecycle.Context())
}
func (f *ModelFacade) SaveSearchOrder(order []string) error {
	return f.web.SaveOrder(f.lifecycle.Context(), order)
}
func (f *ModelFacade) SaveSearchChannel(c websearch.SaveCommand) error {
	return f.web.Save(f.lifecycle.Context(), c)
}
func (f *ModelFacade) DeleteSearchChannel(id string) error {
	return f.web.Delete(f.lifecycle.Context(), id)
}
func (f *ModelFacade) TestWebSearch(query string) (websearch.Result, error) {
	return f.web.Search(f.lifecycle.Context(), query, 3)
}
