package opensciskill

import (
	"archive/zip"
	_ "embed"
	"io/fs"
	"strings"
	"sync"
)

//go:embed defaultskills.zip
var bundledArchive string

var bundled = &compressedResources{}

// The ZIP central directory stays in memory; contents decompress on each read.
// No files are extracted to disk and no complete decompressed copy is retained.
// The existing fs.FS/resource APIs, paths and content hashes remain unchanged.
type compressedResources struct {
	once        sync.Once
	reader      *zip.Reader
	err         error
	catalogOnce sync.Once
	skills      []Info
	diagnostics []string
}

func (r *compressedResources) initialize() error {
	r.once.Do(func() { r.reader, r.err = zip.NewReader(strings.NewReader(bundledArchive), int64(len(bundledArchive))) })
	return r.err
}

func (r *compressedResources) Open(name string) (fs.File, error) {
	if err := r.initialize(); err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return r.reader.Open(name)
}

func (r *compressedResources) ReadFile(name string) ([]byte, error) {
	if err := r.initialize(); err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	return fs.ReadFile(r.reader, name)
}

func (r *compressedResources) catalog() ([]Info, []string) {
	// Default resources are immutable for the process lifetime. Discover and
	// hash them once, not once for every project or policy invalidation. Only
	// metadata is cached; user/project/installed sources are still rescanned.
	r.catalogOnce.Do(func() { r.skills, r.diagnostics = scanFS(r, DefaultRoot, OriginDefault, true) })
	snapshot := cloneSnapshot(Snapshot{Skills: r.skills, Diagnostics: r.diagnostics})
	return snapshot.Skills, snapshot.Diagnostics
}
