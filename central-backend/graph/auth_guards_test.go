package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/odigos-io/odigos/central-backend/graph/model"
	"github.com/odigos-io/odigos/central-backend/internal/logger"
	"github.com/odigos-io/odigos/central-backend/internal/services"
	"github.com/odigos-io/odigos/central-backend/internal/utils"
	"github.com/odigos-io/odigos/central-backend/internal/utils/tokentest"
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// fakeKeycloak answers the admin API with empty-but-valid responses and records
// every write so tests can prove a rejected call changed nothing.
type fakeKeycloak struct {
	mu     sync.Mutex
	writes []string
}

func (f *fakeKeycloak) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// newAuthTestResolver wires real services to a fake Keycloak. initialized controls
// whether Keycloak reports a real user (i.e. first-run setup is over).
func newAuthTestResolver(t *testing.T, initialized bool) (*Resolver, *fakeKeycloak) {
	t.Helper()
	logger.InitLogger()

	fake := &fakeKeycloak{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/master/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "admin-token", "expires_in": 60})
	})
	mux.HandleFunc("GET /admin/realms/master/users", func(w http.ResponseWriter, r *http.Request) {
		users := []map[string]any{{"id": "bootstrap", "username": "admin"}}
		if initialized {
			users = append(users, map[string]any{"id": "u1", "username": "existing@example.com", "email": "existing@example.com"})
		}
		_ = json.NewEncoder(w).Encode(users)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("[]"))
			return
		}
		fake.mu.Lock()
		fake.writes = append(fake.writes, r.Method+" "+r.URL.Path)
		fake.mu.Unlock()
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv("KEYCLOAK_HOST", server.URL)

	adminService := services.NewAdminService(&services.AdminCredentials{Username: "admin", Password: "admin"})
	userService := services.NewUserService(adminService)
	roleMappingService := services.NewRoleMappingService(adminService)
	return &Resolver{
		AdminService:       adminService,
		AuthService:        services.NewAuthService(adminService, userService, roleMappingService),
		UserService:        userService,
		TeamService:        services.NewTeamService(adminService),
		RoleMappingService: roleMappingService,
	}, fake
}

func testJWT(t *testing.T, roles ...string) string {
	t.Helper()
	return tokentest.Install(t).Token(t, "guard-test-user", roles...)
}

type caller struct {
	name string
	ctx  func(t *testing.T) context.Context
}

var (
	anonymous  = caller{"anonymous", func(*testing.T) context.Context { return context.Background() }}
	viewer     = caller{"viewer", roleCaller("viewer")}
	maintainer = caller{"maintainer", roleCaller("maintainer")}
	admin      = caller{"admin", roleCaller("admin")}
	owner      = caller{"owner", roleCaller("owner")}
)

func roleCaller(role string) func(t *testing.T) context.Context {
	return func(t *testing.T) context.Context {
		return utils.SetTokenInContext(context.Background(), testJWT(t, role))
	}
}

// isAuthError also matches by message because some resolvers (cloud connectors)
// re-wrap errors as gqlerror values, which drops the sentinel from the chain.
func isAuthError(err error) bool {
	if errors.Is(err, utils.ErrUnauthenticated) || errors.Is(err, utils.ErrForbidden) {
		return true
	}
	var gqlErr *gqlerror.Error
	return errors.As(err, &gqlErr) &&
		(gqlErr.Message == utils.ErrUnauthenticated.Error() || gqlErr.Message == utils.ErrForbidden.Error())
}

func samlInput() model.SamlIdentityProviderInput {
	return model.SamlIdentityProviderInput{
		ProviderID:             model.AuthProviderIDOkta,
		IdpEntityID:            "https://idp.example.com/metadata",
		SingleSignOnServiceURL: "https://idp.example.com/sso",
		SigningCertificate:     "MIIC",
	}
}

func oidcInput() model.OidcIdentityProviderInput {
	return model.OidcIdentityProviderInput{
		ProviderID:   model.AuthProviderIDGoogle,
		ClientID:     "client",
		ClientSecret: "secret",
		DiscoveryURL: "https://idp.example.com/.well-known/openid-configuration",
	}
}

// guardedOperations covers every resolver that changes Keycloak or reads
// access-management data. Each call returns only the resolver error.
var guardedOperations = []struct {
	name    string
	allowed []caller
	call    func(r *Resolver, ctx context.Context) error
}{
	{
		name:    "ensureSamlIdentityProvider",
		allowed: []caller{admin, owner},
		call: func(r *Resolver, ctx context.Context) error {
			_, err := (&mutationResolver{r}).EnsureSamlIdentityProvider(ctx, samlInput())
			return err
		},
	},
	{
		name:    "ensureOidcIdentityProvider",
		allowed: []caller{admin, owner},
		call: func(r *Resolver, ctx context.Context) error {
			// The OIDC discovery fetch is not faked, so success is not asserted here;
			// only whether the call got past the guard.
			_, err := (&mutationResolver{r}).EnsureOidcIdentityProvider(ctx, oidcInput())
			if err != nil && !isAuthError(err) {
				return nil
			}
			return err
		},
	},
	{
		name:    "updateComputePlatform",
		allowed: []caller{admin, owner},
		call: func(r *Resolver, ctx context.Context) error {
			userID, teamID := "u1", "t1"
			_, err := (&mutationResolver{r}).UpdateComputePlatform(ctx, "cp-1", model.WriteComputePlatformInput{
				AddUserIds: []*string{&userID},
				AddTeamIds: []*string{&teamID},
			})
			if err != nil && !isAuthError(err) {
				return nil
			}
			return err
		},
	},
	{
		name:    "identityProviders",
		allowed: []caller{admin, owner},
		call: func(r *Resolver, ctx context.Context) error {
			_, err := (&queryResolver{r}).IdentityProviders(ctx)
			return err
		},
	},
	{
		name:    "apiTokens",
		allowed: []caller{admin, owner},
		call: func(r *Resolver, ctx context.Context) error {
			_, err := (&queryResolver{r}).APITokens(ctx)
			return err
		},
	},
	{
		name:    "users",
		allowed: []caller{viewer, maintainer, admin, owner},
		call: func(r *Resolver, ctx context.Context) error {
			_, err := (&queryResolver{r}).Users(ctx)
			return err
		},
	},
	{
		name:    "teams",
		allowed: []caller{viewer, maintainer, admin, owner},
		call: func(r *Resolver, ctx context.Context) error {
			_, err := (&queryResolver{r}).Teams(ctx)
			return err
		},
	},
}

func TestGuardedOperationsByRole(t *testing.T) {
	license := testJWT(t)
	t.Setenv(k8sconsts.OdigosOnpremTokenEnvName, license)

	for _, op := range guardedOperations {
		for _, c := range []caller{anonymous, viewer, maintainer, admin, owner} {
			allowed := false
			for _, a := range op.allowed {
				allowed = allowed || a.name == c.name
			}
			t.Run(fmt.Sprintf("%s/%s", op.name, c.name), func(t *testing.T) {
				r, fake := newAuthTestResolver(t, true)
				err := op.call(r, c.ctx(t))
				if allowed {
					if err != nil {
						t.Fatalf("expected success, got %v", err)
					}
					return
				}
				if !isAuthError(err) {
					t.Fatalf("expected an auth error, got %v", err)
				}
				if n := fake.writeCount(); n != 0 {
					t.Fatalf("rejected call still wrote to Keycloak %d times: %v", n, fake.writes)
				}
			})
		}
	}
}

// Before any user exists the sign-in page sets up SSO without a session; once
// a user exists the same anonymous call must be rejected.
func TestIdentityProviderFirstRunSetup(t *testing.T) {
	t.Run("anonymous allowed before initialization", func(t *testing.T) {
		r, fake := newAuthTestResolver(t, false)
		if _, err := (&mutationResolver{r}).EnsureSamlIdentityProvider(context.Background(), samlInput()); err != nil {
			t.Fatalf("expected first-run setup to succeed, got %v", err)
		}
		if fake.writeCount() == 0 {
			t.Fatal("expected the identity provider to be written")
		}
	})

	t.Run("anonymous rejected after initialization", func(t *testing.T) {
		r, fake := newAuthTestResolver(t, true)
		_, err := (&mutationResolver{r}).EnsureSamlIdentityProvider(context.Background(), samlInput())
		if !errors.Is(err, utils.ErrUnauthenticated) {
			t.Fatalf("expected ErrUnauthenticated, got %v", err)
		}
		if n := fake.writeCount(); n != 0 {
			t.Fatalf("rejected call still wrote to Keycloak: %v", fake.writes)
		}
	})

	t.Run("anonymous rejected when initialization cannot be read", func(t *testing.T) {
		r, fake := newAuthTestResolver(t, false)
		t.Setenv("KEYCLOAK_HOST", "http://127.0.0.1:1")
		r.AdminService = services.NewAdminService(&services.AdminCredentials{Username: "admin", Password: "admin"})
		_, err := (&mutationResolver{r}).EnsureSamlIdentityProvider(context.Background(), samlInput())
		if !errors.Is(err, utils.ErrUnauthenticated) {
			t.Fatalf("expected ErrUnauthenticated, got %v", err)
		}
		if n := fake.writeCount(); n != 0 {
			t.Fatalf("rejected call still wrote to Keycloak: %v", fake.writes)
		}
	})
}

// publicOperations are reachable without a session by design; each resolver
// says why in a comment. Every other Query and Mutation resolver must reject an
// anonymous caller before touching any service.
var publicOperations = map[string]bool{
	"SignIn":                      true,
	"SetInitialPassword":          true,
	"VerifyToken":                 true,
	"SystemConfig":                true,
	"ConfiguredIdentityProviders": true,
	"SignInURL":                   true,
	"SignOutURL":                  true,
	"Health":                      true,
	// Fans out to RemoteFetch, which rejects anonymous callers per proxy.
	// TODO(PLAT-1904): remove once RemoteMultiFetch has its own guard.
	"RemoteMultiFetch": true,
}

func TestEveryResolverRejectsAnonymousCallers(t *testing.T) {
	// Only AdminService is wired (for the first-run check); any other service
	// reached by an anonymous call is nil and panics, which fails the test.
	full, _ := newAuthTestResolver(t, true)
	r := &Resolver{AdminService: full.AdminService}

	// Walk the generated interfaces so every schema field is covered, including
	// resolvers added later.
	resolvers := []struct {
		iface reflect.Type
		impl  any
	}{
		{reflect.TypeOf((*MutationResolver)(nil)).Elem(), &mutationResolver{r}},
		{reflect.TypeOf((*QueryResolver)(nil)).Elem(), &queryResolver{r}},
	}
	for _, res := range resolvers {
		v := reflect.ValueOf(res.impl)
		for i := 0; i < res.iface.NumMethod(); i++ {
			name := res.iface.Method(i).Name
			if publicOperations[name] {
				continue
			}
			method := v.MethodByName(name)
			t.Run(name, func(t *testing.T) {
				args := []reflect.Value{reflect.ValueOf(context.Background())}
				for j := 1; j < method.Type().NumIn(); j++ {
					args = append(args, reflect.Zero(method.Type().In(j)))
				}

				var err error
				func() {
					defer func() {
						if p := recover(); p != nil {
							t.Fatalf("anonymous call reached a service without an auth check: %v", p)
						}
					}()
					out := method.Call(args)
					if e, ok := out[len(out)-1].Interface().(error); ok {
						err = e
					}
				}()
				if !isAuthError(err) {
					t.Fatalf("expected an auth error for an anonymous caller, got %v", err)
				}
			})
		}
	}
}
