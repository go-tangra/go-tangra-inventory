package agentrelease

// Platform identifies the artifact an agent can install.
type Platform struct {
	OS          string `json:"os"`           // linux | windows
	Arch        string `json:"arch"`         // amd64 | arm64
	InstallType string `json:"install_type"` // deb | rpm | binary
}

// Install types.
const (
	InstallDeb    = "deb"
	InstallRPM    = "rpm"
	InstallBinary = "binary"
)

// Valid reports whether p is a supported platform (Windows agents are
// always binary installs).
func (p Platform) Valid() bool {
	switch p.Arch {
	case "amd64", "arm64":
	default:
		return false
	}
	switch p.OS {
	case "linux":
		return p.InstallType == InstallDeb || p.InstallType == InstallRPM || p.InstallType == InstallBinary
	case "windows":
		return p.InstallType == InstallBinary
	}
	return false
}

func (p Platform) String() string { return p.OS + "/" + p.Arch + " " + p.InstallType }
