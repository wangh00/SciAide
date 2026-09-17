package skillrun

// Snapshot is the minimum immutable Skill identity needed by downstream
// runtimes. It deliberately excludes catalog implementation details.
type Snapshot struct {
	Name        string `json:"name"`
	ContentHash string `json:"contentHash"`
	PackageHash string `json:"packageHash"`
}
