package wails

import (
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/wangh00/SciAide/internal/app/pythonenv"
)

type PythonFacade struct {
	lifecycle *LifecycleContext
	service   *pythonenv.Service
	kernels   *pythonenv.KernelService
}

func NewPythonFacade(lifecycle *LifecycleContext, service *pythonenv.Service, kernels ...*pythonenv.KernelService) *PythonFacade {
	facade := &PythonFacade{lifecycle: lifecycle, service: service}
	if len(kernels) > 0 {
		facade.kernels = kernels[0]
	}
	return facade
}

func (f *PythonFacade) DetectInterpreters() (pythonenv.Discovery, error) {
	return f.service.DetectInterpreters(f.lifecycle.Context(), "")
}

func (f *PythonFacade) ChooseBaseInterpreter() (pythonenv.Discovery, error) {
	return f.chooseInterpreter("选择用于创建项目环境的基础 Python")
}

func (f *PythonFacade) ChooseExistingEnvironment() (pythonenv.Discovery, error) {
	return f.chooseInterpreter("选择已有虚拟环境中的 python.exe")
}

func (f *PythonFacade) chooseInterpreter(title string) (pythonenv.Discovery, error) {
	path, err := runtime.OpenFileDialog(f.lifecycle.Context(), runtime.OpenDialogOptions{
		Title:   title,
		Filters: []runtime.FileFilter{{DisplayName: "Python executable", Pattern: "python.exe;python3.exe;python"}},
	})
	if err != nil {
		return pythonenv.Discovery{}, err
	}
	if path == "" {
		return pythonenv.Discovery{Status: "cancelled", Message: "已取消选择解释器", Interpreters: []pythonenv.Interpreter{}}, nil
	}
	return f.service.DetectInterpreters(f.lifecycle.Context(), path)
}

func (f *PythonFacade) GetProjectEnvironment(projectID string) (pythonenv.Environment, error) {
	return f.service.Get(f.lifecycle.Context(), projectID)
}

func (f *PythonFacade) CreateProjectEnvironment(projectID, baseInterpreterPath string, rebuild bool) (pythonenv.Environment, error) {
	return f.service.Create(f.lifecycle.Context(), projectID, baseInterpreterPath, rebuild)
}

func (f *PythonFacade) BindProjectEnvironment(projectID, interpreterPath string) (pythonenv.Environment, error) {
	return f.service.BindExternal(f.lifecycle.Context(), projectID, interpreterPath)
}

func (f *PythonFacade) VerifyProjectEnvironment(projectID string) (pythonenv.Environment, error) {
	return f.service.Verify(f.lifecycle.Context(), projectID)
}

func (f *PythonFacade) DeleteProjectEnvironment(projectID string) error {
	return f.service.Delete(f.lifecycle.Context(), projectID)
}

func (f *PythonFacade) StopProjectKernel(projectID string) error {
	if f.kernels == nil {
		return nil
	}
	return f.kernels.Stop(projectID)
}
