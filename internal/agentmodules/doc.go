// Package agentmodules is the agent side of feature 037: it checks a
// delivered module bundle (a zip rendered by the gateway, holding the
// module's compose file, rendered configuration, a join token and store
// passwords), writes it below the locally configured modules directory and
// runs the locally configured deploy hook.
//
// Security role: the server chooses only the module name, re-validated here
// as a single path component; directory, owner, group and hook come from
// the local configuration only. The archive is checked strictly before
// anything is written (regular files and directories only, every path
// clean and below <module>/, no duplicates, at most 300 entries and 16 MiB
// uncompressed); an existing <dir>/<module> is never touched
// (already_installed). The tree is staged in <dir>/.staging-<module>-<random>
// on an os.Root, files created with O_EXCL|O_NOFOLLOW, then renamed into
// place; secrets (.env, private/) are written 0600 before any byte lands.
// The hook passes the checks of the certificate deploy hook
// (agentcerts.CheckHook) and runs with a fixed environment. The core runs
// over interfaces and is held to 100 % test coverage.
package agentmodules
