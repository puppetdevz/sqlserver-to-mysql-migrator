package matcher

import "strings"

// TableNameMatcher centralizes table-name matching rules.
type TableNameMatcher struct {
	caseSensitive bool
}

func NewTableNameMatcher(caseSensitive bool) TableNameMatcher {
	return TableNameMatcher{caseSensitive: caseSensitive}
}

func DefaultTableNameMatcher() TableNameMatcher {
	return NewTableNameMatcher(true)
}

func (m TableNameMatcher) CaseSensitive() bool {
	return m.caseSensitive
}

func (m TableNameMatcher) Key(name string) string {
	key := strings.TrimSpace(name)
	if m.caseSensitive {
		return key
	}
	return strings.ToUpper(key)
}

func (m TableNameMatcher) Equal(a, b string) bool {
	return m.Key(a) == m.Key(b)
}

func (m TableNameMatcher) BuildSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[m.Key(name)] = struct{}{}
	}
	return set
}
