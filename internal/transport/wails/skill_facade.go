package wails

import (
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/skill"
)

type SkillFacade struct {
	lifecycle *LifecycleContext
	service   *skill.Service
}

func NewSkillFacade(lifecycle *LifecycleContext, service *skill.Service) *SkillFacade {
	return &SkillFacade{lifecycle: lifecycle, service: service}
}

func (f *SkillFacade) RefreshSkills() (skill.RefreshResult, error) {
	return f.service.Refresh(f.lifecycle.Context())
}

func (f *SkillFacade) ListInstalledSkills() ([]skill.InstalledSkill, error) {
	return f.service.ListInstalled(f.lifecycle.Context())
}

func (f *SkillFacade) ChooseSkillFolder() (string, error) {
	return runtime.OpenDirectoryDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{Title: "选择 Skill 文件夹"})
}

func (f *SkillFacade) ChooseSkillZIP() (string, error) {
	return runtime.OpenFileDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{
		Title: "选择 Skill ZIP 包",
		Filters: []runtime.FileFilter{
			{DisplayName: "ZIP 压缩包 (*.zip)", Pattern: "*.zip"},
		},
	})
}

func (f *SkillFacade) SetProjectSkill(request skill.SetProjectSkillCommand) (skill.ProjectSkillView, error) {
	return f.service.SetProjectSkill(f.lifecycle.Context(), request)
}

func (f *SkillFacade) ListProjectSkills(projectID string) ([]skill.ProjectSkillView, error) {
	return f.service.ListProjectSkills(f.lifecycle.Context(), projectID)
}

func (f *SkillFacade) InstallSkill(request skill.InstallCommand) (skill.InstallResult, error) {
	return f.service.Install(f.lifecycle.Context(), request)
}

func (f *SkillFacade) UninstallSkill(request skill.UninstallCommand) (skill.UninstallResult, error) {
	return f.service.Uninstall(f.lifecycle.Context(), request)
}

func (f *SkillFacade) RollbackProjectSkill(request skill.RollbackProjectSkillCommand) (skill.RollbackProjectSkillResult, error) {
	return f.service.RollbackProjectSkill(f.lifecycle.Context(), request)
}
