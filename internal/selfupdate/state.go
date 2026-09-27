package selfupdate

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

// Phases of the upgrade state file (contracts/agent-cli.md).
const (
	PhaseInstalling      = "installing"       // agent: staged and verified, helper starting
	PhaseAwaitingConfirm = "awaiting_confirm" // helper: installing / waiting for the new version
	PhaseConfirmed       = "confirmed"        // new agent: connected, submitted, reported
	PhaseRollingBack     = "rolling_back"     // helper: restoring the previous version
	PhaseRolledBack      = "rolled_back"      // helper: previous version restored
	PhaseFailed          = "failed"           // helper: refused before replacing anything, or restore failed
)

// maxStateBytes bounds the state file (manifest <= 64 KiB).
const maxStateBytes = 256 << 10

// State is the upgrade state file shared by the agent, the helper and the
// new agent (0600 in the staging directory).
type State struct {
	RequestID        string                `json:"request_id"`
	FromVersion      string                `json:"from_version"`
	ToVersion        string                `json:"to_version"`
	InstallType      string                `json:"install_type"`
	Platform         agentrelease.Platform `json:"platform"`
	Artifact         string                `json:"artifact"`
	ArtifactSHA256   string                `json:"artifact_sha256"`
	RollbackArtifact string                `json:"rollback_artifact,omitempty"`
	RollbackSHA256   string                `json:"rollback_sha256,omitempty"`
	PreviousBinary   string                `json:"previous_binary,omitempty"`
	Executable       string                `json:"executable"`
	Manifest         []byte                `json:"manifest"`
	Signature        []byte                `json:"signature"`
	KeyID            string                `json:"key_id"`
	AllowDowngrade   bool                  `json:"allow_downgrade,omitempty"`
	ConfirmTimeout   time.Duration         `json:"confirm_timeout"`
	Phase            string                `json:"phase"`
	Deadline         time.Time             `json:"deadline"`
	Reason           string                `json:"reason,omitempty"`
}

func (u *Updater) saveState(st State) error {
	b, _ := json.Marshal(st) // strings, bytes, times and a platform always marshal
	return u.fs.WriteFileAtomic(u.path(stateFile), b, 0o600)
}

var errStateTooLarge = errors.New("selfupdate: state file too large")

func (u *Updater) loadState() (State, error) {
	var st State
	b, err := u.fs.ReadFile(u.path(stateFile))
	if err != nil {
		return st, err
	}
	if len(b) > maxStateBytes {
		return st, errStateTooLarge
	}
	err = json.Unmarshal(b, &st)
	return st, err
}
