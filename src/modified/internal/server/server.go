// Package server provides the HTTP server for Seam AMS.
package server

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ha1tch/seam-ui/internal/alerts"
	"github.com/ha1tch/seam-ui/internal/auth"
	"github.com/ha1tch/seam-ui/internal/config"
	"github.com/ha1tch/seam-ui/internal/handlers/api"
	"github.com/ha1tch/seam-ui/internal/handlers/middleware"
	"github.com/ha1tch/seam-ui/internal/i18n"
	"github.com/ha1tch/seam-ui/internal/modules"
	"github.com/ha1tch/seam-ui/internal/modules/account"
	"github.com/ha1tch/seam-ui/internal/modules/administration"
	"github.com/ha1tch/seam-ui/internal/modules/assets"
	"github.com/ha1tch/seam-ui/internal/modules/demo"
	"github.com/ha1tch/seam-ui/internal/modules/documents"
	"github.com/ha1tch/seam-ui/internal/modules/issues"
	"github.com/ha1tch/seam-ui/internal/modules/settings"
	"github.com/ha1tch/seam-ui/internal/modules/workorders"
	"github.com/ha1tch/seam-ui/internal/qrcode"
	"github.com/ha1tch/seam-ui/internal/reports"
	"github.com/ha1tch/seam-ui/internal/ui"
	appversion "github.com/ha1tch/seam-ui/internal/version"
	"github.com/ha1tch/seam-ui/internal/webhook"
	webpkg "github.com/ha1tch/seam-ui/web"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// Server represents the Seam AMS HTTP server.
type Server struct {
	cfg         *config.Config
	httpServer  *http.Server
	logger      *slog.Logger
	xoluClient  *xolu.Client
	authSvc     *auth.Service
	webhookSvc  *webhook.Service
	translator  *i18n.Translator
	userService *auth.UserService
}

// New creates a new server instance.
func New(cfg *config.Config) (*Server, error) {
	// Set up logger
	var logHandler slog.Handler
	opts := &slog.HandlerOptions{
		Level: parseLogLevel(cfg.Log.Level),
	}

	if cfg.Log.Format == "json" {
		logHandler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		logHandler = slog.NewTextHandler(os.Stdout, opts)
	}
	logger := slog.New(logHandler)

	// Create XOLU client
	xoluOpts := []xolu.ClientOption{}
	if cfg.XOLU.APIKey != "" {
		xoluOpts = append(xoluOpts, xolu.WithAPIKey(cfg.XOLU.APIKey))
	}
	xoluClient := xolu.New(cfg.XOLU.URL, xoluOpts...)

	// Create auth service (if enabled)
	var authSvc *auth.Service
	if cfg.Auth.Enabled {
		authSvc = auth.NewService(auth.Config{
			Secret:        cfg.Auth.JWTSecret,
			TokenExpiry:   cfg.Auth.TokenExpiry.Duration(),
			RefreshExpiry: cfg.Auth.RefreshExpiry.Duration(),
			APIKeyHeader:  cfg.Auth.APIKeyHeader,
		})
	}

	// Create webhook service (if enabled)
	var webhookSvc *webhook.Service
	if cfg.Features.EnableWebhooks {
		webhookSvc = webhook.NewService(webhook.DefaultConfig())
	}

	// Create translator for i18n
	translator, err := i18n.New("en")
	if err != nil {
		return nil, fmt.Errorf("creating translator: %w", err)
	}
	logger.Info("loaded locales", "count", translator.LocaleCount(), "locales", translator.Locales())

	// Create user service for user management
	userStore := auth.NewXOLUUserStore(&xoluClientAdapter{client: xoluClient})
	userService := auth.NewUserService(userStore, auth.DefaultConfig())

	s := &Server{
		cfg:         cfg,
		logger:      logger,
		xoluClient:  xoluClient,
		authSvc:     authSvc,
		webhookSvc:  webhookSvc,
		translator:  translator,
		userService: userService,
	}

	// Build HTTP handler
	httpHandler, err := s.buildHandler()
	if err != nil {
		return nil, fmt.Errorf("building handler: %w", err)
	}

	// Create HTTP server
	s.httpServer = &http.Server{
		Addr:         cfg.Address(),
		Handler:      httpHandler,
		ReadTimeout:  cfg.Server.ReadTimeout.Duration(),
		WriteTimeout: cfg.Server.WriteTimeout.Duration(),
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	return s, nil
}

// buildHandler constructs the HTTP handler chain.
func (s *Server) buildHandler() (http.Handler, error) {
	mux := http.NewServeMux()

	// API routes
	if s.cfg.Features.EnableAPI {
		// Create QR generator if enabled
		var qrGen *qrcode.Generator
		if s.cfg.Features.EnableQR {
			qrGen = qrcode.NewGenerator(qrcode.Config{
				Prefix:   s.cfg.Features.QRPrefix,
				Length:   12,
				Checksum: true,
				BaseURL:  s.cfg.Features.QRBaseURL,
			})
		}

		// Create reports service
		reportsSvc := reports.NewService(s.xoluClient)

		// Create alerts service
		alertsSvc := alerts.NewService(s.xoluClient)

		// Create auth middleware for API protection
		var authMiddleware *auth.Middleware
		if s.authSvc != nil {
			authMiddleware = auth.NewMiddleware(s.authSvc, "X-API-Key")
		}

		apiRouter := api.NewRouter(api.RouterConfig{
			XOLUClient:           s.xoluClient,
			AuthService:          s.authSvc,
			AuthMiddleware:       authMiddleware,
			WebhookService:       s.webhookSvc,
			QRGenerator:          qrGen,
			ReportsService:       reportsSvc,
			AlertsService:        alertsSvc,
			Locales:              []string{"en", "es", "pt"},
			RateLimit:            s.cfg.Features.RateLimitPerMin,
			TenantFromClaimsOnly: s.cfg.Auth.TenantFromClaimsOnly,
		})
		apiHandler := apiRouter.Handler(api.RouterConfig{
			RateLimit:      s.cfg.Features.RateLimitPerMin,
			AllowedOrigins: []string{"*"},
		})
		mux.Handle("/api/", apiHandler)
	}

	// UI routes
	if s.cfg.Features.EnableUI {
		uiCfg := ui.DefaultConfig()
		uiCfg.AppName = "Seam AMS"
		uiCfg.Version = appversion.Version
		uiCfg.DevMode = s.cfg.Features.DevMode
		uiCfg.UseVendoredJS = s.cfg.Features.VendorJS
		uiCfg.EnableOperational = s.cfg.Features.EnableOperational
		uiCfg.EnableAdmin = s.cfg.Features.EnableAdmin
		uiCfg.EnableWorkOrders = s.cfg.Features.EnableWorkOrders
		uiCfg.EnableDemo = s.cfg.Features.EnableDemo
		uiCfg.SeamAPIURL = s.cfg.Features.SeamAPIURL

		// SEAM_FORCE_LANGUAGE: checked early, as the initial Locale value
		// only. Deliberately not a general override -- if the normal
		// bootup process ever gains the configuration and data needed to
		// load a tenant's saved Settings locale, that runs after this and
		// is free to overwrite it; this just supplies a starting default
		// where none would otherwise exist.
		//
		// Resolved via Translator.ResolveLocale, not a plain HasLocale
		// check -- supports both directions of base/region matching
		// (Horacio, 2026-07-29): a requested base code ("en") matches a
		// loaded region variant ("en_gb") if no plain "en" is loaded, and
		// a requested region code ("es_uy") matches a loaded base
		// ("es") or a sibling region ("es_es") if no exact "es_uy" is
		// loaded. Whatever locale in that language family is available
		// gets loaded regardless of direction, with a warning when it
		// isn't an exact match -- only a value with nothing in its
		// language family loaded at all is dropped and logged as unknown.
		if s.cfg.Features.ForceLanguage != "" {
			if s.translator != nil {
				if resolved, exact := s.translator.ResolveLocale(s.cfg.Features.ForceLanguage); resolved != "" {
					uiCfg.Locale = resolved
					if !exact {
						s.logger.Warn("SEAM_FORCE_LANGUAGE resolved to a different loaded locale",
							"requested", s.cfg.Features.ForceLanguage, "loaded", resolved)
					}
				} else {
					s.logger.Warn("SEAM_FORCE_LANGUAGE set to an unknown locale, ignoring",
						"value", s.cfg.Features.ForceLanguage)
				}
			}
		}

		// Log warning if DevMode is enabled
		if uiCfg.DevMode {
			s.logger.Warn("UI DevMode is enabled - development fallbacks active. DO NOT use in production!")
		}

		// Create session manager
		var sessionMgr *auth.SessionManager
		if s.cfg.Auth.Enabled {
			sessionMgr = auth.NewSessionManager(auth.SessionConfig{
				Secret:   s.cfg.Auth.SessionSecret,
				Duration: s.cfg.Auth.SessionDuration.Duration(),
				Secure:   s.cfg.Server.TLSCert != "", // Use secure cookies if TLS enabled
			})
		}

		// Create CSRF manager
		var csrfMgr *auth.CSRFManager
		if s.cfg.Auth.Enabled && s.cfg.Auth.CSRFEnabled {
			csrfMgr = auth.NewCSRFManager(auth.CSRFConfig{
				Secure: s.cfg.Server.TLSCert != "",
			})
		}

		// Create login limiter for brute-force protection
		var loginLimiter *auth.LoginLimiter
		if s.cfg.Auth.Enabled {
			loginLimiter = auth.NewLoginLimiter(auth.DefaultLoginLimiterConfig())
		}

		// Create audit logger for security events
		var auditLogger *auth.AuditLogger
		if s.cfg.Auth.Enabled {
			var auditStore auth.AuditStore
			if s.xoluClient != nil {
				auditStore = auth.NewXOLUAuditStore(s.xoluClient)
			}
			auditLogger = auth.NewAuditLogger(s.logger, auditStore)
		}

		// Create password reset manager
		var resetMgr *auth.PasswordResetManager
		if s.cfg.Auth.Enabled {
			resetMgr = auth.NewPasswordResetManager(auth.DefaultPasswordResetConfig())
		}

		uiRouter := ui.NewRouter(s.xoluClient, uiCfg, s.translator, s.userService, sessionMgr, csrfMgr, loginLimiter, auditLogger, resetMgr)

		// Module registry (MOD-4, MODULE_SPEC.md §3.2): registered here,
		// same place every other UI collaborator (session/CSRF/audit) is
		// already wired in. Account, Settings, and Administration
		// (2026-08-04, replacing the former single "System" module,
		// per Horacio's decision -- this is what T-02 was asking for)
		// join Assets (MOD-6) and Documents (MOD-7) as real, non-nil
		// MountRoutes registrations: internal/modules/{assets,account,
		// settings,administration,documents}.MountRoutes(uiRouter) are
		// each a closure over uiRouter itself, calling its exported
		// MountXRoutes(mux) method -- see those methods' own doc
		// comments for why this is safe (same handlers, same private
		// auth/CSRF middleware, registered exactly once, on this mux --
		// the same one uiRouter is mounted on below -- not uiRouter's
		// own separate private mux).
		//
		// Account's HasAccess is unconditionally true, deliberately --
		// Profile has to be reachable by every authenticated user
		// regardless of role, so this is the one module whose access
		// control genuinely can't live at the module boundary; real
		// gating for Settings/Administration happens both at the module
		// level (HasModuleAccess, below) and per-route
		// (requirePerm(auth.PermSettings*/PermAdministrationUser*),
		// inside MountSettingsRoutes/MountAdministrationRoutes
		// themselves) -- the module-level check controls only whether
		// the nav entry appears at all, not a substitute for the
		// route-level ones.
		registry := modules.NewRegistry()
		registry.Register(modules.Module{
			ID:          "assets",
			Label:       "nav.module.assets",
			Icon:        "inventory_2",
			Group:       "apps",
			DashboardID: "assets-dashboard",
			Path:        "/",
			HasAccess:   func(role string) bool { return auth.HasModuleAccess(role, "assets") },
			MountRoutes: assets.MountRoutes(uiRouter),
			// SidebarItems (2026-08-06, SIDEBAR_SYNC_PLAN_2026-08-06.md):
			// exactly MainNav's own former static 19-item list,
			// redistributed here rather than rewritten -- same items,
			// same order, same section groupings, same icons. "/map"
			// is included unconditionally here; its own runtime-mutable
			// visibility is filtered at render time
			// (activeModuleSidebarItems, internal/ui/layout.go), not
			// here, since EnableMap can change per request without a
			// server restart.
			SidebarItems: []modules.SidebarItem{
				{Label: "nav.section.operational", Section: true},
				{Label: "nav.dashboard", URL: "/", Icon: "dashboard"},
				{Label: "nav.assets", URL: "/assets", Icon: "inventory"},
				{Label: "nav.events", URL: "/events", Icon: "timeline"},
				{Label: "nav.sensors", URL: "/sensors", Icon: "sensors"},
				{Label: "nav.alerts", URL: "/alerts", Icon: "notifications_active"},
				{Label: "nav.map", URL: "/map", Icon: "map"},

				{Label: "nav.section.configuration", Section: true},
				{Label: "nav.asset_types", URL: "/asset-types", Icon: "category"},
				{Label: "nav.rules", URL: "/rules", Icon: "rule"},
				{Label: "nav.fsm_editor", URL: "/fsm/editor", Icon: "device_hub"},

				{Label: "nav.section.masters", Section: true},
				{Label: "nav.brands", URL: "/brands", Icon: "business"},
				{Label: "nav.models", URL: "/models", Icon: "precision_manufacturing"},
				{Label: "nav.regions", URL: "/regions", Icon: "public"},
				{Label: "nav.locations", URL: "/locations", Icon: "place"},
				{Label: "nav.custodians", URL: "/custodians", Icon: "manage_accounts"},
				{Label: "nav.currencies", URL: "/currencies", Icon: "payments"},
				{Label: "nav.cost_centers", URL: "/cost-centers", Icon: "account_balance"},
				{Label: "nav.alert_types", URL: "/alert-types", Icon: "warning"},
				{Label: "nav.properties", URL: "/properties", Icon: "tune"},
			},
		})
		registry.Register(modules.Module{
			ID:          "documents",
			Label:       "nav.module.documents",
			Icon:        "description",
			Group:       "apps",
			DashboardID: "documents-dashboard",
			Path:        "/documents",
			HasAccess:   func(role string) bool { return auth.HasModuleAccess(role, "documents") },
			MountRoutes: documents.MountRoutes(uiRouter),
		})
		registry.Register(modules.Module{
			ID:               "account",
			Label:            "nav.module.account",
			Icon:             "person",
			Group:            "system",
			Path:             "/profile",
			HasAccess:        func(role string) bool { return true },
			MountRoutes:      account.MountRoutes(uiRouter),
			HideFromSelector: true, // layout.go's user-avatar dropdown already links straight to /profile -- a second entry here would be redundant, not helpful (Horacio, 2026-08-04)
		})
		registry.Register(modules.Module{
			ID:          "settings",
			Label:       "nav.module.settings",
			Icon:        "settings",
			Group:       "system",
			Path:        "/settings",
			HasAccess:   func(role string) bool { return auth.HasModuleAccess(role, "settings") },
			MountRoutes: settings.MountRoutes(uiRouter),
			// SidebarItems requires a leading Section header -- navItems()
			// (internal/ui/layout.go) only attaches a non-Section item to
			// a group that comes after one; a bare item list with no
			// Section renders as an empty <ul>, found and fixed after it
			// silently produced exactly that on this module's own page.
			SidebarItems: []modules.SidebarItem{
				{Label: "nav.section.admin", Section: true},
				{Label: "nav.settings", URL: "/settings", Icon: "settings"},
			},
		})
		registry.Register(modules.Module{
			ID:          "administration",
			Label:       "nav.module.administration",
			Icon:        "admin_panel_settings",
			Group:       "system",
			Path:        "/admin/users",
			HasAccess:   func(role string) bool { return auth.HasModuleAccess(role, "administration") },
			MountRoutes: administration.MountRoutes(uiRouter),
			SidebarItems: []modules.SidebarItem{
				{Label: "nav.section.admin", Section: true},
				{Label: "nav.users", URL: "/admin/users", Icon: "people"},
			},
		})
		// Work Orders playground (2026-08-04, T-67's follow-on) --
		// registered unconditionally like every other module (registry
		// entries are cheap, static Go values), but genuinely inert
		// unless EnableWorkOrders is true. HasAccess checks the flag
		// directly rather than deriving from a permission namespace --
		// no "workorders.*" permissions exist yet for any role (T-11's
		// own real scoping would need to design those), so a
		// HasModuleAccess-derived gate would make this module invisible
		// to everyone even when the flag is on, not visible-when-
		// permitted. Checking the flag here, not just in
		// MountWorkOrdersRoutes, matters: without it, the module would
		// show up in the nav even when disabled (real access, no
		// routes to reach) -- exactly the "visible but unmountable"
		// state this session's own adversarial registry tests
		// documented as allowed by the type system, caught here before
		// it shipped as an actual bug rather than a deliberate test
		// case.
		registry.Register(modules.Module{
			ID:          "workorders",
			Label:       "nav.module.workorders",
			Icon:        "assignment",
			Group:       "apps",
			Path:        "/workorders",
			HasAccess:   func(role string) bool { return s.cfg.Features.EnableWorkOrders },
			MountRoutes: workorders.MountRoutes(uiRouter),
			// SidebarItems (2026-08-06, SIDEBAR_SYNC_PLAN_2026-08-06.md):
			// reuses workorders.list_title/workorders.new -- the same
			// i18n keys the list page and its own "New" button already
			// use, not new keys for the same two labels. A leading
			// Section header is required -- navItems() only attaches a
			// non-Section item to a group that comes after one.
			SidebarItems: []modules.SidebarItem{
				{Label: "nav.module.workorders", Section: true},
				{Label: "workorders.list_title", URL: "/workorders", Icon: "assignment"},
				{Label: "workorders.new", URL: "/workorders/new", Icon: "add_circle"},
			},
		})
		// Issue Tracker (2026-08-05) -- simple, always-on, no feature
		// flag, matching MountIssuesRoutes' own reasoning: unlike Work
		// Orders, nothing here needs the "playground, opt-in" framing.
		// HasAccess returns true unconditionally, same reasoning as
		// Account's own registration -- no granular permission model
		// exists yet for this module, and inventing one prematurely
		// for a "simple" tracker would be scope creep ahead of any
		// real need for it.
		registry.Register(modules.Module{
			ID:          "issues",
			Label:       "nav.module.issues",
			Icon:        "bug_report",
			Group:       "apps",
			Path:        "/issues",
			HasAccess:   func(role string) bool { return true },
			MountRoutes: issues.MountRoutes(uiRouter),
			// SidebarItems (2026-08-06, SIDEBAR_SYNC_PLAN_2026-08-06.md):
			// reuses issues.list_title/issues.new, same reasoning as
			// Work Orders above.
			SidebarItems: []modules.SidebarItem{
				{Label: "nav.module.issues", Section: true},
				{Label: "issues.list_title", URL: "/issues", Icon: "bug_report"},
				{Label: "issues.new", URL: "/issues/new", Icon: "add_circle"},
				// Templates are managed by admin/manager only (2026-10-10),
				// reusing system.settings.edit as the gate -- the same
				// permission the settings screens already use for
				// configuration, no new permission namespace invented.
				{Label: "issues.templates", URL: "/issue-templates", Icon: "article", RequiredPerm: string(auth.PermSettingsEdit)},
			},
		})
		// Demo (opt-in, EnableDemo): shows new components and fixes to
		// developers. HasAccess checks the flag directly, like Work Orders,
		// so a disabled module never appears in the nav; any valid role may
		// see it once enabled (requireAuth only, no permission namespace).
		registry.Register(modules.Module{
			ID:          "demo",
			Label:       "nav.module.demo",
			Icon:        "science",
			Group:       "apps",
			Path:        "/demo",
			HasAccess:   func(role string) bool { return s.cfg.Features.EnableDemo && auth.IsValidRole(role) },
			MountRoutes: demo.MountRoutes(uiRouter),
			SidebarItems: []modules.SidebarItem{
				{Label: "nav.module.demo", Section: true},
				{Label: "demo.title", URL: "/demo", Icon: "science"},
			},
		})
		uiRouter.SetModuleRegistry(registry)

		registry.MountAll(mux)
		mux.Handle("/", uiRouter)
	}

	// Static files — served from embedded FS so the binary is self-contained.
	// Changes to web/static/ require a rebuild; for live CSS/JS iteration use
	// the override: SEAM_STATIC_DIR=web/static (reads from disk instead).
	var staticHandler http.Handler
	if override := os.Getenv("SEAM_STATIC_DIR"); override != "" {
		staticHandler = http.FileServer(http.Dir(override))
		s.logger.Info("static files served from disk (dev override)", "dir", override)
	} else {
		sub, err := fs.Sub(webpkg.Static, "static")
		if err != nil {
			panic("embedded static FS misconfigured: " + err.Error())
		}
		staticHandler = http.FileServer(http.FS(sub))
	}
	mux.Handle("/static/", http.StripPrefix("/static/", staticHandler))

	// Health check (always enabled)
	mux.HandleFunc("/health", s.healthCheck)

	// Apply global middleware
	var handler http.Handler = mux

	// Recovery middleware
	handler = middleware.Recoverer(s.logger)(handler)

	// Request logging
	reqLogger := middleware.NewRequestLogger(s.logger)
	handler = reqLogger.Handler(handler)

	return handler, nil
}

// healthCheck handles health check requests.
func (s *Server) healthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy","service":"seam-ams"}`))
}

// Start starts the server and blocks until shutdown.
func (s *Server) Start() error {
	// Channel for shutdown signals
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Channel for server errors
	serverErr := make(chan error, 1)

	// Start server in goroutine
	go func() {
		s.logger.Info("starting server",
			"address", s.cfg.Address(),
			"tls", s.cfg.IsTLS(),
		)

		var err error
		if s.cfg.IsTLS() {
			err = s.httpServer.ListenAndServeTLS(
				s.cfg.Server.TLSCert,
				s.cfg.Server.TLSKey,
			)
		} else {
			err = s.httpServer.ListenAndServe()
		}

		if err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for shutdown signal or error
	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	case sig := <-shutdown:
		s.logger.Info("shutdown signal received", "signal", sig.String())
	}

	// Graceful shutdown
	return s.Shutdown()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown() error {
	s.logger.Info("shutting down server")

	ctx, cancel := context.WithTimeout(
		context.Background(),
		s.cfg.Server.ShutdownTimeout.Duration(),
	)
	defer cancel()

	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown error: %w", err)
	}

	s.logger.Info("server stopped")
	return nil
}

// parseLogLevel converts a string log level to slog.Level.
func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// xoluClientAdapter adapts xolu.Client to auth.XOLUClient interface.
type xoluClientAdapter struct {
	client *xolu.Client
}

func (a *xoluClientAdapter) Get(ctx context.Context, entityType string, id int64) (*auth.XOLUEntity, error) {
	entity, err := a.client.Get(ctx, entityType, id)
	if err != nil {
		return nil, err
	}
	return &auth.XOLUEntity{
		ID:        entity.ID,
		Data:      entity.Data,
		CreatedAt: entity.CreatedAt,
		UpdatedAt: entity.UpdatedAt,
	}, nil
}

func (a *xoluClientAdapter) Create(ctx context.Context, entityType string, data map[string]interface{}) (*auth.XOLUEntity, error) {
	entity, err := a.client.Create(ctx, entityType, data)
	if err != nil {
		return nil, err
	}
	return &auth.XOLUEntity{
		ID:        entity.ID,
		Data:      entity.Data,
		CreatedAt: entity.CreatedAt,
		UpdatedAt: entity.UpdatedAt,
	}, nil
}

func (a *xoluClientAdapter) Update(ctx context.Context, entityType string, id int64, data map[string]interface{}) (*auth.XOLUEntity, error) {
	entity, err := a.client.Update(ctx, entityType, id, data)
	if err != nil {
		return nil, err
	}
	return &auth.XOLUEntity{
		ID:        entity.ID,
		Data:      entity.Data,
		CreatedAt: entity.CreatedAt,
		UpdatedAt: entity.UpdatedAt,
	}, nil
}

func (a *xoluClientAdapter) Delete(ctx context.Context, entityType string, id int64) error {
	return a.client.Delete(ctx, entityType, id)
}

func (a *xoluClientAdapter) OQL(ctx context.Context, query string) (*auth.OQLResult, error) {
	result, err := a.client.OQL(ctx, query)
	if err != nil {
		return nil, err
	}
	return &auth.OQLResult{
		Data: result.Data,
	}, nil
}
