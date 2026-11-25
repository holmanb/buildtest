package patch

import (
	"github.com/holmanb/buildtest/internals/overlord/state"
)

func init() {
	patches[2] = []PatchFunc{patch2}
}

// patch2 historically migrated legacy identities into the state's
// "identities" data. Identity management has been removed from Buildtest,
// so this patch is now a no-op; it must remain registered so that the
// patch level of existing state files stays consistent.
func patch2(s *state.State) error {
	return nil
}
