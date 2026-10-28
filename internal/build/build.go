package build

// default build-time variables, overridden via -ldflags -X at link time
var (
	GitSHA    = "unknown"
	BuildTime = "unknown"
)
