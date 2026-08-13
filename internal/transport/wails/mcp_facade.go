package wails

import "github.com/wangh00/SciAide/internal/app/mcpserver"

type MCPFacade struct {
	lifecycle *LifecycleContext
	service   *mcpserver.Service
}

func NewMCPFacade(lifecycle *LifecycleContext, service *mcpserver.Service) *MCPFacade {
	return &MCPFacade{lifecycle: lifecycle, service: service}
}

func (f *MCPFacade) SaveMCPServer(request mcpserver.SaveCommand) (mcpserver.Server, error) {
	value, err := f.service.Save(f.lifecycle.Context(), request)
	return publicMCPServer(value), err
}

func (f *MCPFacade) ImportMCPServers(request mcpserver.ImportCommand) (mcpserver.ImportResult, error) {
	result, err := f.service.Import(f.lifecycle.Context(), request)
	for i := range result.Imported {
		result.Imported[i] = publicMCPServer(result.Imported[i])
	}
	return result, err
}

func (f *MCPFacade) ListMCPServers() ([]mcpserver.Server, error) {
	values, err := f.service.List(f.lifecycle.Context())
	for i := range values {
		values[i] = publicMCPServer(values[i])
	}
	return values, err
}

func (f *MCPFacade) ConnectMCPServer(serverID string) (mcpserver.Server, error) {
	value, err := f.service.Connect(f.lifecycle.Context(), serverID)
	return publicMCPServer(value), err
}

func (f *MCPFacade) DisconnectMCPServer(serverID string) (mcpserver.Server, error) {
	value, err := f.service.Disconnect(f.lifecycle.Context(), serverID)
	return publicMCPServer(value), err
}

func publicMCPServer(value mcpserver.Server) mcpserver.Server {
	configured := make(map[string]bool, len(value.SecretEnv))
	for name := range value.SecretEnv {
		configured[name] = true
	}
	value.SecretEnv = map[string]string{}
	value.SecretConfigured = configured
	return value
}

func (f *MCPFacade) RemoveMCPServer(serverID string) error {
	return f.service.Delete(f.lifecycle.Context(), serverID)
}

func (f *MCPFacade) GetMCPCapabilities(serverID string) (mcpserver.CapabilitySnapshot, error) {
	return f.service.Capabilities(f.lifecycle.Context(), serverID)
}
