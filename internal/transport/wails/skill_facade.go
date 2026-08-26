package wails

import (
	"github.com/wangh00/SciAide/internal/opensciskill"
	"github.com/wangh00/SciAide/internal/platform/folderopen"
)

type SkillFacade struct {
	lifecycle *LifecycleContext
	dynamic   *opensciskill.Service
}

func NewSkillFacade(lifecycle *LifecycleContext, dynamic *opensciskill.Service) *SkillFacade {
	return &SkillFacade{lifecycle: lifecycle, dynamic: dynamic}
}

func (f *SkillFacade) ListSkills(projectID string) (opensciskill.Snapshot, error) {
	return f.dynamic.Catalog(f.lifecycle.Context(), projectID)
}

func (f *SkillFacade) SetSkillEnabled(projectID, name string, enabled bool) (opensciskill.Info, error) {
	return f.dynamic.SetEnabled(f.lifecycle.Context(), projectID, name, enabled)
}

func (f *SkillFacade) SetAllSkillsEnabled(projectID string, enabled bool) (opensciskill.PolicyBatchResult, error) {
	return f.dynamic.SetAllEnabled(f.lifecycle.Context(), projectID, enabled)
}

func (f *SkillFacade) WriteUserSkill(name, content string) (opensciskill.Info, error) {
	return f.dynamic.WriteUser(f.lifecycle.Context(), name, content)
}

func (f *SkillFacade) ReadUserSkill(name string) (string, error) {
	return f.dynamic.ReadUser(name)
}

func (f *SkillFacade) ListSkillSource(projectID, name string) (opensciskill.SourceTree, error) {
	return f.dynamic.ListSource(f.lifecycle.Context(), projectID, name)
}

func (f *SkillFacade) ReadSkillSourceFile(projectID, name, sourcePath string) (opensciskill.SourceFile, error) {
	return f.dynamic.ReadSourceFile(f.lifecycle.Context(), projectID, name, sourcePath)
}

func (f *SkillFacade) GetRunSkillRoutingAudit(projectID, runID string) (opensciskill.RoutingAudit, error) {
	return f.dynamic.GetRoutingAudit(f.lifecycle.Context(), projectID, runID)
}

func (f *SkillFacade) GetSkillRoutingMetrics(projectID string) (opensciskill.RoutingMetrics, error) {
	return f.dynamic.RoutingMetrics(f.lifecycle.Context(), projectID)
}

func (f *SkillFacade) DeleteUserSkill(name string) error {
	return f.dynamic.DeleteUser(f.lifecycle.Context(), name)
}

func (f *SkillFacade) RefreshDynamicSkills(projectID string) (opensciskill.Snapshot, error) {
	f.dynamic.Invalidate()
	return f.dynamic.Catalog(f.lifecycle.Context(), projectID)
}

func (f *SkillFacade) InstallGitSkills(request opensciskill.InstallGitRequest) (opensciskill.InstallGitResult, error) {
	return f.dynamic.InstallGit(f.lifecycle.Context(), request)
}

func (f *SkillFacade) RemoveInstalledSkill(namespace, name string) (opensciskill.RemoveInstalledResult, error) {
	return f.dynamic.RemoveInstalled(f.lifecycle.Context(), namespace, name)
}

func (f *SkillFacade) OpenUserSkillsFolder() error {
	_, user := f.dynamic.Roots()
	return folderopen.Open(user)
}

func (f *SkillFacade) OpenInstalledSkillsFolder() error {
	installed, _ := f.dynamic.Roots()
	return folderopen.Open(installed)
}
