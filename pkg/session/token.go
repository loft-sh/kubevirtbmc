package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
)

type contextKey string

const (
	userContextKey contextKey = "user"
)

var ts TokenStore

type TokenStore struct {
	rwMutex sync.RWMutex
	store   map[string]TokenInfo
}

type TokenInfo struct {
	ID       string
	Username string
}

func NewTokenInfo(id, username string) TokenInfo {
	return TokenInfo{
		ID:       id,
		Username: username,
	}
}

func init() {
	ts = TokenStore{
		store: make(map[string]TokenInfo, 1),
	}

}

func generateToken(tokenInfo TokenInfo) string {
	tokenJSON, _ := json.Marshal(tokenInfo)

	hash := sha256.Sum256(tokenJSON)
	return hex.EncodeToString(hash[:])
}

func AddToken(tokenInfo TokenInfo) string {
	ts.rwMutex.Lock()
	defer ts.rwMutex.Unlock()

	token := generateToken(tokenInfo)
	ts.store[token] = tokenInfo

	return token
}

func GetToken(token string) (TokenInfo, bool) {
	ts.rwMutex.RLock()
	defer ts.rwMutex.RUnlock()

	tokenInfo, exists := ts.store[token]

	return tokenInfo, exists
}

// RemoveSession deletes the session with the given session ID and reports
// whether it existed. The store is keyed by token, not by session ID, so
// deleting by ID requires the reverse lookup: passing a session ID to
// RemoveToken silently does nothing.
func RemoveSession(sessionID string) bool {
	ts.rwMutex.Lock()
	defer ts.rwMutex.Unlock()

	for token, tokenInfo := range ts.store {
		if tokenInfo.ID == sessionID {
			delete(ts.store, token)
			return true
		}
	}

	return false
}

func RemoveToken(token string) {
	ts.rwMutex.Lock()
	defer ts.rwMutex.Unlock()

	delete(ts.store, token)
}

// ListSessions returns the currently live sessions, ordered by session ID so
// the Sessions collection is stable between reads. The token itself is never
// returned: only the caller that created a session ever sees it.
func ListSessions() []TokenInfo {
	ts.rwMutex.RLock()
	defer ts.rwMutex.RUnlock()

	sessions := make([]TokenInfo, 0, len(ts.store))
	for _, tokenInfo := range ts.store {
		sessions = append(sessions, tokenInfo)
	}
	slices.SortFunc(sessions, func(a, b TokenInfo) int {
		return strings.Compare(a.ID, b.ID)
	})

	return sessions
}

func GetTokenFromSessionID(sessionID string) (TokenInfo, bool) {
	ts.rwMutex.RLock()
	defer ts.rwMutex.RUnlock()

	for _, tokenInfo := range ts.store {
		if tokenInfo.ID == sessionID {
			return tokenInfo, true
		}
	}

	return TokenInfo{}, false
}

func AuthMiddleware(bmcUser, bmcPassword string) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()

			if ok {
				if user != bmcUser || pass != bmcPassword {
					w.Header().Set("WWW-Authenticate", `Basic realm="Redfish"`)
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				ctx := context.WithValue(r.Context(), userContextKey, user)
				r = r.WithContext(ctx)
				next.ServeHTTP(w, r)
				return
			}

			token := r.Header.Get("X-Auth-Token")
			if token == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="Redfish"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			ts.rwMutex.RLock()
			_, exists := ts.store[token]
			ts.rwMutex.RUnlock()

			if !exists {
				w.Header().Set("WWW-Authenticate", `Basic realm="Redfish"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
