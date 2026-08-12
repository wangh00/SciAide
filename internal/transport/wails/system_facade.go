package wails

type SystemFacade struct {
	version string
}

type SystemInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Phase   string `json:"phase"`
}

func NewSystemFacade(version string) *SystemFacade {
	return &SystemFacade{version: version}
}

func (f *SystemFacade) GetSystemInfo() SystemInfo {
	return SystemInfo{Name: "SciAide", Version: f.version, Phase: "P1"}
}
