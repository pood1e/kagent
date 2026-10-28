package execution

import (
	"net/http"
	"slices"
	"strings"
	"unicode"
)

// RuntimeAuthorization is an HTTP ext-authz decision over native effective
// scopes. The gateway supplies the Actor JWT as Authorization and a fixed scope
// requirement; successful decisions expose neither user tokens nor credentials.
func (s *Service) RuntimeAuthorization(w http.ResponseWriter, r *http.Request) {
	required := r.URL.Query().Get("scope")
	if len(required) > 256 || strings.ContainsFunc(required, unicode.IsSpace) {
		http.Error(w, "invalid scope requirement", http.StatusBadRequest)
		return
	}
	result, err := s.RuntimeAuthority(r.Context())
	if err != nil || required != "" && !slices.Contains(result.EffectiveScopes, required) {
		http.Error(w, "runtime execution denied", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}
