package cli

import (
	"fmt"
	"strings"
)

// oneOwner validates a flag that takes a single owner, and returns it as
// CODEOWNERS would write it.
//
// The vocabulary is CODEOWNERS': `@org/storage` is a team, `@alice` a person,
// and an email address is either. Nothing here checks that the owner exists —
// a team that appears in no rule is a legitimate thing to name, and the point
// of an authored field is that it can say something GitHub does not know.
//
// What it does check is that one owner was given. A comma is the mistake worth
// catching: the observed list is the one with several owners in it, and a flag
// that quietly stored "@org/a,@org/b" as a single owner would match nothing
// and look right.
func oneOwner(flag, raw string) (string, error) {
	owner := strings.TrimSpace(raw)
	if strings.ContainsAny(owner, ", ") {
		return "", fmt.Errorf("--%s takes one owner, not a list: %q", flag, raw)
	}
	// The @ is how CODEOWNERS writes an owner, and how everything that reads
	// one expects it. Accepting a bare `org/storage` and storing it as typed
	// would make two spellings of one owner, which is a comparison that fails
	// silently later.
	if !strings.HasPrefix(owner, "@") && !strings.Contains(owner, "@") {
		owner = "@" + owner
	}
	return owner, nil
}
