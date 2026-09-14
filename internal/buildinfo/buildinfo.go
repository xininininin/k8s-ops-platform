package buildinfo

// Version and Source are injected at image build time. Defaults keep local
// builds reproducible without requiring linker flags.
var (
	Version = "dev"
	Source  = "local"
)
