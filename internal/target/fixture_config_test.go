package target

import "strings"

func fixtureMatchesPort(addr, binding string) bool { return addr == strings.TrimSpace(binding) }
