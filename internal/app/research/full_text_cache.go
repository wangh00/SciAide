package research

import (
	"github.com/wangh00/SciAide/internal/document"
	"sync"
	"time"
)

type fullTextEntry struct {
	parsed document.Parsed
	sha    string
	at     time.Time
	bytes  int
}
type fullTextCache struct {
	mu      sync.Mutex
	entries map[string]fullTextEntry
	bytes   int
}

func (c *fullTextCache) get(key string) (document.Parsed, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.entries[key]
	if ok && time.Since(v.at) > 30*time.Minute {
		delete(c.entries, key)
		c.bytes -= v.bytes
		ok = false
	}
	return v.parsed, v.sha, ok
}

func (c *fullTextCache) put(key string, p document.Parsed, sha string) {
	size := 0
	for _, u := range p.Units {
		size += len(u.Content) + len(u.Title) + len(u.Locator) + 128
	}
	if size > 2<<20 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]fullTextEntry{}
	}
	if old, ok := c.entries[key]; ok {
		c.bytes -= old.bytes
		delete(c.entries, key)
	}
	for len(c.entries) >= 16 || c.bytes+size > 8<<20 {
		oldest := ""
		var at time.Time
		for k, v := range c.entries {
			if oldest == "" || v.at.Before(at) {
				oldest = k
				at = v.at
			}
		}
		if oldest == "" {
			break
		}
		c.bytes -= c.entries[oldest].bytes
		delete(c.entries, oldest)
	}
	c.entries[key] = fullTextEntry{p, sha, time.Now(), size}
	c.bytes += size
}
