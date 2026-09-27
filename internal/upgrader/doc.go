// Package upgrader is the operating-system glue of agent self-upgrade
// (feature 023): install-type detection commands, the transient systemd unit
// that runs the verified staged helper outside the agent's cgroup, dpkg/rpm
// installs and rollbacks, the atomic binary swap with .prev backup and the
// Windows service stop/replace/start sequence.
//
// Security role: every command runs with a fixed argument list and no shell;
// only artifacts that internal/selfupdate has verified are passed in. Like
// internal/collector this package is excluded from the unit coverage gate and
// is exercised by the container e2e job (make e2e-upgrade); its argument
// construction is unit-tested with a fake runner.
package upgrader
