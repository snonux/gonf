package resource

// Init eagerly initializes the resource repository. The repository is
// otherwise created lazily on first use, so Init is optional; callers use it
// to warm up state (or fail fast) before registering resources.
func Init() {
	getRepository()
}
