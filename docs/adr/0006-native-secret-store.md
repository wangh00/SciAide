# ADR-0006：模型密钥使用操作系统 SecretStore

- 状态：已采纳（Windows）；macOS/Linux Adapter 待对应平台阶段实现
- 日期：2026-08-12

Windows 生产构建通过 `advapi32.dll` 的 Credential Manager API 保存 API Key，凭据目标名使用 `SciAide:<secret_ref>`。SQLite 的 `model_profiles` 只保存不可逆引用 `secret_ref`。

Wails API 只提供设置/替换、删除、配置状态和掩码，不提供读取明文密钥。React 不把 Key 放入浏览器存储、日志或长期状态；保存请求完成后立即清空输入。

测试使用进程内 `Memory` Adapter，该实现不得进入生产 Composition Root。非 Windows 构建当前明确返回“不支持”，禁止自动回退为明文文件。
