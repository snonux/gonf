package remote

import "sync"

// The Pusher's per-platform ("goos/goarch") cross-build cache and build
// locks: quick, buildMu-guarded accessors that buildGonf (crossbuild.go)
// uses to reuse a binary already built this run and to keep two goroutines
// from building the same platform at once.

// buildKeyLock returns p's build lock for key ("goos/goarch"), creating it
// on first use. Fanout (fleet.go) pushes to every target concurrently, so
// buildGonf can be entered by several goroutines at once for several
// DIFFERENT platforms. A single lock held across the whole build used to
// serialize all of them — an unrelated openbsd/arm64 cross-compile would sit
// blocked behind an in-flight linux/amd64 one for no reason. Locking per key
// instead lets unrelated platforms build fully in parallel, while still
// serializing two goroutines that race to build the SAME key (avoiding a
// duplicate, wasted cross-compile and a racy cache write/read).
func (p *Pusher) buildKeyLock(key string) *sync.Mutex {
	p.buildMu.Lock()
	defer p.buildMu.Unlock()
	mu, ok := p.buildKeyLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		p.buildKeyLocks[key] = mu
	}
	return mu
}

func (p *Pusher) buildCacheGet(key string) (cachedBuild, bool) {
	p.buildMu.Lock()
	defer p.buildMu.Unlock()
	c, ok := p.buildCache[key]
	return c, ok
}

func (p *Pusher) buildCacheSet(key string, c cachedBuild) {
	p.buildMu.Lock()
	defer p.buildMu.Unlock()
	p.buildCache[key] = c
}
