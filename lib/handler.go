package lib

import (
	"net/http"
	"strings"

	"github.com/rs/cors"
	"go.uber.org/zap"
	"golang.org/x/net/webdav"
)

type handlerUser struct {
	User
	webdav.Handler
}

type Handler struct {
	user  *handlerUser
	users map[string]*handlerUser
}

func NewHandler(c *Config) (http.Handler, error) {
	h := &Handler{
		user: &handlerUser{
			User: User{
				Permissions: c.Permissions,
			},
			Handler: webdav.Handler{
				Prefix: c.Prefix,
				FileSystem: Dir{
					Dir:     webdav.Dir(c.Scope),
					noSniff: c.NoSniff,
				},
				LockSystem: webdav.NewMemLS(),
			},
		},
		users: map[string]*handlerUser{},
	}

	for _, u := range c.Users {
		h.users[u.Username] = &handlerUser{
			User: u,
			Handler: webdav.Handler{
				Prefix: c.Prefix,
				FileSystem: Dir{
					Dir:     webdav.Dir(u.Scope),
					noSniff: c.NoSniff,
				},
				LockSystem: webdav.NewMemLS(),
			},
		}
	}

	if c.CORS.Enabled {
		return cors.New(cors.Options{
			AllowCredentials:   c.CORS.Credentials,
			AllowedOrigins:     c.CORS.AllowedHosts,
			AllowedMethods:     c.CORS.AllowedMethods,
			AllowedHeaders:     c.CORS.AllowedHeaders,
			OptionsPassthrough: false,
		}).Handler(h), nil
	}

	return h, nil
}

// ServeHTTP determines if the request is for this plugin, and if all prerequisites are met.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user := h.user

	// Authentication
	if len(h.users) > 0 {
		w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)

		// Gets the correct user for this request.
		username, password, ok := r.BasicAuth()
		zap.L().Info("login attempt", zap.String("username", username), zap.String("remote_address", r.RemoteAddr))
		if !ok {
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		user, ok = h.users[username]
		if !ok {
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		if !user.checkPassword(password) {
			zap.L().Info("invalid password", zap.String("username", username), zap.String("remote_address", r.RemoteAddr))
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		zap.L().Info("user authorized", zap.String("username", username))
	}

	// Checks for user permissions relatively to this PATH.
	allowed := user.Allowed(r)

	zap.L().Debug("allowed & method & path", zap.Bool("allowed", allowed), zap.String("method", r.Method), zap.String("path", r.URL.Path))

	if !allowed {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if r.Method == "HEAD" {
		w = responseWriterNoBody{w}
	}

	// Excerpt from RFC4918, section 9.4:
	//
	// 		GET, when applied to a collection, may return the contents of an
	//		"index.html" resource, a human-readable view of the contents of
	//		the collection, or something else altogether.
	//
	// Get, when applied to collection, will return the same as PROPFIND method.
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, user.Prefix) {
		info, err := user.FileSystem.Stat(r.Context(), strings.TrimPrefix(r.URL.Path, user.Prefix))
		if err == nil && info.IsDir() {
			r.Method = "PROPFIND"

			if r.Header.Get("Depth") == "" {
				r.Header.Add("Depth", "1")
			}
		}
	}

	// Runs the WebDAV.
	user.ServeHTTP(w, r)
}

type responseWriterNoBody struct {
	http.ResponseWriter
}

func (w responseWriterNoBody) Write(data []byte) (int, error) {
	return 0, nil
}
