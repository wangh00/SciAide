// Package httpua supplies browser User-Agent headers for application HTTP calls.
package httpua

import (
	"hash/fnv"
	"net/http"
	"strings"
)

var pool = [...]string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/85.0.4183.102 Safari/537.36 Edg/85.0.564.51",
}

// ForHost keeps retries and sessions on the same host consistent. It does not
// change TLS fingerprints, cookies, rate limits, or other browser behavior.
func ForHost(host string) string {
	return pool[Index(host)]
}

func Index(host string) int {
	if strings.EqualFold(host, "idp.nature.com") {
		host = "www.nature.com"
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSuffix(host, "."))))
	return int(h.Sum32() % uint32(len(pool)))
}

func Apply(request *http.Request) {
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	for key := range request.Header {
		if strings.EqualFold(key, "User-Agent") {
			delete(request.Header, key)
		}
	}
	request.Header.Set("User-Agent", ForHost(request.URL.Hostname()))
}
