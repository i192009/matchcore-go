package transport

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Principal struct {
	Account string `json:"account"`
	Role    string `json:"role"`
}
type Credential struct {
	Key     string `json:"key"`
	Account string `json:"account"`
	Role    string `json:"role"`
}
type identityKey struct{}
type Auth struct {
	identities map[[32]byte]Principal
	mu         sync.Mutex
	limits     map[[32]byte]*rate.Limiter
}

func LoadAuth() (*Auth, error) {
	var credentials []Credential
	if e := json.Unmarshal([]byte(os.Getenv("API_KEYS")), &credentials); e != nil {
		return nil, errors.New("API_KEYS must be a JSON array of key/account/role objects")
	}
	if len(credentials) == 0 {
		return nil, errors.New("API_KEYS cannot be empty")
	}
	a := &Auth{identities: map[[32]byte]Principal{}, limits: map[[32]byte]*rate.Limiter{}}
	for _, c := range credentials {
		if len(c.Key) < 16 || c.Account == "" || (c.Role != "trader" && c.Role != "admin") {
			return nil, errors.New("invalid API key configuration")
		}
		h := sha256.Sum256([]byte(c.Key))
		if _, exists := a.identities[h]; exists {
			return nil, errors.New("duplicate API key")
		}
		a.identities[h] = Principal{c.Account, c.Role}
		a.limits[h] = rate.NewLimiter(200, 400)
	}
	return a, nil
}
func (a *Auth) Check(token string) (Principal, error) {
	h := sha256.Sum256([]byte(strings.TrimPrefix(token, "Bearer ")))
	p, ok := a.identities[h]
	if !ok {
		return p, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	a.mu.Lock()
	allowed := a.limits[h].Allow()
	a.mu.Unlock()
	if !allowed {
		return p, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
	return p, nil
}
func (a *Auth) Interceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	m, _ := metadata.FromIncomingContext(ctx)
	carrier := propagation.MapCarrier{}
	for k, v := range m {
		if len(v) > 0 {
			carrier[k] = v[0]
		}
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	ctx, span := otel.Tracer("matchcore/grpc").Start(ctx, info.FullMethod)
	defer span.End()
	tokens := m.Get("authorization")
	if len(tokens) != 1 {
		return nil, status.Error(codes.Unauthenticated, "authorization required")
	}
	p, e := a.Check(tokens[0])
	if e != nil {
		return nil, e
	}
	return handler(context.WithValue(ctx, identityKey{}, p), req)
}
func identity(ctx context.Context) Principal { p, _ := ctx.Value(identityKey{}).(Principal); return p }
