package matcher

import "strings"

// QuoteIdent quotes a MySQL identifier. Backticks inside the name are doubled.
// An empty name becomes “, which the server rejects; the client does not invent a name.
func QuoteIdent(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}
