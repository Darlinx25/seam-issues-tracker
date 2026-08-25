package ui

import (
	"crypto/subtle"
	"net/http"

	"github.com/ha1tch/seam-ui/internal/auth"
	"github.com/ha1tch/seam-ui/internal/i18n"
	"github.com/ha1tch/seam-ui/internal/modules"
	xolu "github.com/ha1tch/xolu/pkg/client"
)

// Router sets up all UI routes.
type Router struct {
	mux            *http.ServeMux
	handler        *UIHandler
	config         Config
	translator     *i18n.Translator
	userService    *auth.UserService
	sessionManager *auth.SessionManager
	csrfManager    *auth.CSRFManager
	loginLimiter   *auth.LoginLimiter
	auditLogger    *auth.AuditLogger
	resetManager   *auth.PasswordResetManager
}

// NewRouter creates a new UI router.
func NewRouter(client *xolu.Client, cfg Config, translator *i18n.Translator, userService *auth.UserService, sessionMgr *auth.SessionManager, csrfMgr *auth.CSRFManager, loginLimiter *auth.LoginLimiter, auditLogger *auth.AuditLogger, resetMgr *auth.PasswordResetManager) *Router {
	r := &Router{
		mux:            http.NewServeMux(),
		handler:        NewUIHandler(client, cfg, translator, userService, sessionMgr, csrfMgr, loginLimiter, auditLogger, resetMgr),
		config:         cfg,
		translator:     translator,
		userService:    userService,
		sessionManager: sessionMgr,
		csrfManager:    csrfMgr,
		loginLimiter:   loginLimiter,
		auditLogger:    auditLogger,
		resetManager:   resetMgr,
	}
	r.registerRoutes()
	return r
}

// SetModuleRegistry wires a *modules.Registry into the router's underlying
// page handler, so the module selector (MODULE_SPEC.md §4) has data to
// render. r.handler is unexported (internal/server can't reach it
// directly), so this delegates on the caller's behalf -- same reasoning
// PageHandler.SetModuleRegistry itself gives for being a setter rather
// than a constructor parameter: not disrupting NewRouter's already-long
// signature for a stage (MOD-4) that doesn't require every caller to
// 
// supply one.
func (r *Router) SetModuleRegistry(reg *modules.Registry) {
	r.handler.page.SetModuleRegistry(reg)
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// requireAuth wraps a handler to require authentication.
// Redirects to /login if no valid session exists.
// Also validates CSRF tokens on state-changing requests.
func (r *Router) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// Skip auth check if session manager not configured
		if r.sessionManager == nil {
			next.ServeHTTP(w, req)
			return
		}

		session, err := r.sessionManager.ValidateSession(req)
		if err != nil {
			// Redirect to login with return URL
			returnURL := req.URL.Path
			if req.URL.RawQuery != "" {
				returnURL += "?" + req.URL.RawQuery
			}
			http.Redirect(w, req, "/login?next="+returnURL, http.StatusSeeOther)
			return
		}

		// Validate CSRF on state-changing requests
		if r.csrfManager != nil {
			switch req.Method {
			case "POST", "PUT", "PATCH", "DELETE":
				if err := r.csrfManager.ValidateToken(req); err != nil {
					// Audit log: CSRF violation
					if r.auditLogger != nil {
						r.auditLogger.LogCSRFViolation(req.Context(), req, session.UserID)
					}
					r.handler.ErrorCSRF(w, req)
					return
				}
			}
		}

		// Add session claims to context
		ctx := auth.ContextWithClaims(req.Context(), session.ToClaims())
		next.ServeHTTP(w, req.WithContext(ctx))
	}
}

// requireAuthRawBody is like requireAuth but validates CSRF only from the
// request header, never calling ParseForm. Use for endpoints where the
// request body is not a form (e.g. Content-Type: image/svg+xml).
// Calling r.FormValue() on such requests discards the body before the
// handler can read it.
func (r *Router) requireAuthRawBody(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.sessionManager == nil {
			next.ServeHTTP(w, req)
			return
		}
		session, err := r.sessionManager.ValidateSession(req)
		if err != nil {
			returnURL := req.URL.Path
			if req.URL.RawQuery != "" {
				returnURL += "?" + req.URL.RawQuery
			}
			http.Redirect(w, req, "/login?next="+returnURL, http.StatusSeeOther)
			return
		}
		// CSRF: header only — do not call ParseForm which would consume the body
		if r.csrfManager != nil {
			switch req.Method {
			case "POST", "PUT", "PATCH", "DELETE":
				providedToken := req.Header.Get(auth.CSRFHeaderName)
				cookie, cookieErr := req.Cookie(auth.CSRFCookieName)
				if cookieErr != nil || providedToken == "" {
					if r.auditLogger != nil {
						r.auditLogger.LogCSRFViolation(req.Context(), req, session.UserID)
					}
					r.handler.ErrorCSRF(w, req)
					return
				}
				if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(providedToken)) != 1 {
					if r.auditLogger != nil {
						r.auditLogger.LogCSRFViolation(req.Context(), req, session.UserID)
					}
					r.handler.ErrorCSRF(w, req)
					return
				}
			}
		}
		ctx := auth.ContextWithClaims(req.Context(), session.ToClaims())
		next.ServeHTTP(w, req.WithContext(ctx))
	}
}

// requirePerm wraps a handler to require a specific permission.
// Must be used after requireAuth.
func (r *Router) requirePerm(perm auth.Permission, next http.HandlerFunc) http.HandlerFunc {
	return r.requireAuth(func(w http.ResponseWriter, req *http.Request) {
		claims, ok := auth.ClaimsFromContext(req.Context())
		if !ok {
			http.Redirect(w, req, "/login", http.StatusSeeOther)
			return
		}

		if !auth.HasPermission(claims.Role, perm) {
			// Audit log: access denied
			if r.auditLogger != nil {
				r.auditLogger.LogAccessDenied(req.Context(), req, claims.UserID, req.URL.Path, string(perm))
			}
			r.handler.Error403(w, req)
			return
		}

		next.ServeHTTP(w, req)
	})
}

// registerRoutes registers all UI routes.
func (r *Router) registerRoutes() {
	// Public routes (no auth required)
	r.mux.HandleFunc("GET /login", r.handler.LoginPage)
	r.mux.HandleFunc("POST /login", r.handler.LoginSubmit)
	r.mux.HandleFunc("GET /logout", r.handler.Logout)

	// Password reset (public)
	r.mux.HandleFunc("GET /forgot-password", r.handler.ForgotPasswordPage)
	r.mux.HandleFunc("POST /forgot-password", r.handler.ForgotPasswordSubmit)
	r.mux.HandleFunc("GET /reset-password", r.handler.ResetPasswordPage)
	r.mux.HandleFunc("POST /reset-password", r.handler.ResetPasswordSubmit)

	// QR Scan (public - for scanning QR codes)
	r.mux.HandleFunc("GET /s/{code}", r.handler.QRScan)

	// Static files (public)
	staticFS := http.FileServer(http.Dir("web/static"))
	r.mux.Handle("GET /static/", http.StripPrefix("/static/", staticFS))

	// Protected routes (require auth)

	// Asset-picker search (T-19, 2026-08-04) -- a shared utility
	// endpoint, not tied to any one module: the Asset form's own
	// parent_id field and the Sensor bind page both call it. Kept
	// inline here rather than moved into a MountXRoutes method for
	// that reason -- it doesn't belong to Assets, Sensors, or any
	// other single module more than the others.
	r.mux.HandleFunc("GET /picker/search", r.requireAuth(r.handler.PickerSearch))

	// Gallery — backend for the "image" formengine widget (T-10), same
	// reasoning as /picker/search directly above: a generic, cross-
	// module endpoint (any owner_type in galleryOwnerTypes, not tied to
	// Assets specifically despite that being the only real consumer
	// today), kept inline here rather than under MountAssetRoutes.
	// GalleryUpload is multipart/form-data -- requireAuth (not
	// requireAuthRawBody) is correct here, not requireAuthRawBody's
	// raw-body case: Go's own r.FormValue (called inside
	// csrfManager.ValidateToken) already parses multipart bodies
	// transparently and caches the result, so the handler's own later
	// r.ParseMultipartForm call is a safe, cheap no-op, not a second
	// real parse. GalleryDelete has no body at all (id is a query
	// param) -- CSRF for it comes through ValidateToken's own header
	// fallback (X-CSRF-Token), which the widget's JS sets explicitly
	// since there's no form body for a bodyless DELETE to carry it in.
	r.mux.HandleFunc("GET /gallery/items", r.requireAuth(r.handler.GalleryList))
	r.mux.HandleFunc("POST /gallery/upload", r.requireAuth(r.handler.GalleryUpload))
	r.mux.HandleFunc("DELETE /gallery/item", r.requireAuth(r.handler.GalleryDelete))
	r.mux.HandleFunc("GET /gallery/blob", r.requireAuth(r.handler.GalleryBlobProxy))

	// Dashboard, Assets, Events, Sensors, Alerts, Map, FSM Editor, Rules,
	// Asset Types, and every master table are registered by
	// MountAssetRoutes (MOD-6), called from internal/modules/assets via
	// internal/server/server.go -- not here. See MountAssetRoutes' own
	// doc comment for why the call site moved but the handlers/
	// middleware didn't (same reasoning as MountAccountRoutes/
	// MountSettingsRoutes/MountAdministrationRoutes, MOD-5's original
	// single MountSystemRoutes before T-02 split it three ways).

	// User Profile, Settings, and Administration are registered by
	// MountAccountRoutes/MountSettingsRoutes/MountAdministrationRoutes
	// respectively (T-02, 2026-08-04, splitting the former single
	// MountSystemRoutes), each called from its own internal/modules/*
	// package via internal/server/server.go -- not here.
}

// MountSystemRoutes registers the System module's routes -- User Profile
// (always available), Settings and Administration (gated behind
// r.config.EnableAdmin, exactly as they were when registered inline here),
// and the System dashboard itself (GET /system, new in MOD-5) -- onto mux.
//
// This is MOD-5's answer to MODULE_SPEC.md §6.1's "only the registration
// call site moves" pattern (written for Assets, applied here to System):
// the handler logic (r.handler.Settings etc.) and the auth/CSRF/permission
// middleware (r.requireAuth, r.requirePerm) are exactly what they always
// were -- unchanged, still private to this package, still the same
// Router instance's own session/CSRF state. Only WHERE these HandleFunc
// calls happen moved, from inline in registerRoutes() (r.mux, this
// Router's own private mux) to here, called via a closure internal/
// server/server.go constructs (system.MountRoutes(uiRouter)) against
// mux -- the server's shared top-level mux (modules.Registry's own
// stated contract), not r.mux. Registered exactly once: registerRoutes()
// no longer registers any of these paths, so there is no duplicate
// registration and no shadowed/dead route on either mux.
// MountAccountRoutes registers the Account module's routes -- User
// Profile only. Unconditional, no permission gate at all beyond being
// authenticated (MODULE_SPEC.md §5.1's original rule for the Profile
// view, preserved unchanged): every user needs to reach their own
// profile regardless of role, so this is deliberately the one module
// whose HasAccess is always true.
//
// Split out from the former MountSystemRoutes (MOD-5/MOD-8) 2026-08-04,
// per Horacio's decision: Account, Settings, and Administration are
// three separate modules, not one -- specifically because bundling
// Profile with anything permission-gated forces the combined module's
// own HasAccess to stay true for everyone, pushing all real access
// control down into per-route checks instead of the module boundary
// itself (unlike Assets/Documents, which gate at the module level).
// Splitting preserves the permission granularity system.settings.*/
// system.user.* already have, rather than collapsing it back down.
func (r *Router) MountAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /profile", r.requireAuth(r.handler.Profile))
	mux.HandleFunc("POST /profile", r.requireAuth(r.handler.ProfileSave))
	mux.HandleFunc("POST /profile/preferences", r.requireAuth(r.handler.ProfilePreferencesSave))
	mux.HandleFunc("GET /profile/password", r.requireAuth(r.handler.PasswordChange))
	mux.HandleFunc("POST /profile/password", r.requireAuth(r.handler.PasswordChangeSave))
}

// MountSettingsRoutes registers the Settings module's routes -- tenant
// configuration (general/notifications/features). Gated behind
// r.config.EnableAdmin (same flag Settings/Administration always
// shared, unchanged by this split) and, per route, system.settings.*
// -- a permission namespace that already existed independently of
// system.user.*, not invented for this split. See MountAccountRoutes'
// doc comment for why this is a separate module from Account.
func (r *Router) MountSettingsRoutes(mux *http.ServeMux) {
	if !r.config.EnableAdmin {
		return
	}
	mux.HandleFunc("GET /settings", r.requireAuth(r.handler.Settings))
	mux.HandleFunc("POST /settings/general", r.requirePerm(auth.PermSettingsEdit, r.handler.SettingsSaveGeneral))
	mux.HandleFunc("POST /settings/notifications", r.requirePerm(auth.PermSettingsEdit, r.handler.SettingsSaveNotifications))
	mux.HandleFunc("POST /settings/features", r.requirePerm(auth.PermSettingsEdit, r.handler.SettingsSaveFeatures))
}

// MountAdministrationRoutes registers the Administration module's
// routes -- user/role management. Gated behind r.config.EnableAdmin
// and, per route, system.user.* -- see MountSettingsRoutes' own
// comment for the same reasoning applied to this permission namespace.
func (r *Router) MountAdministrationRoutes(mux *http.ServeMux) {
	if !r.config.EnableAdmin {
		return
	}
	mux.HandleFunc("GET /admin/users", r.requirePerm(auth.PermAdministrationUserView, r.handler.AdminUsersList))
	mux.HandleFunc("GET /admin/users/new", r.requirePerm(auth.PermAdministrationUserCreate, r.handler.AdminUserNew))
	mux.HandleFunc("POST /admin/users", r.requirePerm(auth.PermAdministrationUserCreate, r.handler.AdminUserCreate))
	mux.HandleFunc("GET /admin/users/{id}", r.requirePerm(auth.PermAdministrationUserView, r.handler.AdminUserEdit))
	mux.HandleFunc("POST /admin/users/{id}", r.requirePerm(auth.PermAdministrationUserEdit, r.handler.AdminUserUpdate))
	mux.HandleFunc("GET /admin/users/{id}/password", r.requirePerm(auth.PermAdministrationUserEdit, r.handler.AdminUserPasswordReset))
	mux.HandleFunc("POST /admin/users/{id}/password", r.requirePerm(auth.PermAdministrationUserEdit, r.handler.AdminUserPasswordResetSave))
}

// MountAssetRoutes registers the Assets module's routes -- the
// dashboard (GET /{$}), Assets/Events/Sensors/Alerts/Map (gated behind
// r.config.EnableOperational, exactly as they were when registered
// inline here), and FSM Editor/Rules/Asset Types/every master table
// (gated behind r.config.EnableAdmin, same reasoning) -- onto mux.
//
// This is MOD-6's application of the exact pattern MountSystemRoutes
// (MOD-5) established, per MODULE_SPEC.md §6.1's "only the
// registration call site moves" reasoning and §5.4's concrete
// mechanism: the handler logic and the auth/permission middleware are
// exactly what they always were -- unchanged, still private to this
// package. Only WHERE these HandleFunc calls happen moved, from inline
// in registerRoutes() (r.mux, this Router's own private mux) to here,
// called via a closure internal/server/server.go constructs
// (assets.MountRoutes(uiRouter)) against mux -- the server's shared
// top-level mux, not r.mux. Registered exactly once: registerRoutes()
// no longer registers any of these paths.
//
// By far the largest of the two MountXRoutes methods (98 routes vs
// System's 13) -- confirmed directly against MODULE_SPEC.md §6.1's own
// description ("assets, sensors, rules, alerts, masters... ~3,000+
// lines"), not a narrower reading of "Assets module" than the spec
// intends. The lines below were extracted mechanically from their
// original location in registerRoutes(), not retyped, to eliminate
// transcription risk across a surface this size.
func (r *Router) MountAssetRoutes(mux *http.ServeMux) {
	// Dashboard (unconditional, matching its prior placement outside any
	// feature-flag gate)
	mux.HandleFunc("GET /{$}", r.requireAuth(r.handler.Dashboard))

	// Assets, Events, Sensors, Alerts, Map (SEAM_ENABLE_OPERATIONAL=true)
	if r.config.EnableOperational {
		// Assets
		mux.HandleFunc("GET /assets", r.requireAuth(r.handler.AssetsList))
		mux.HandleFunc("GET /assets/type-fields", r.requireAuth(r.handler.AssetTypeFields))
		mux.HandleFunc("GET /assets/{id}", r.requireAuth(r.handler.AssetDetail))
		mux.HandleFunc("GET /assets/dynamic/new", r.requireAuth(r.handler.AssetNewDynamic))
		mux.HandleFunc("GET /assets/dynamic/{id}/edit", r.requireAuth(r.handler.AssetEditDynamic))
		mux.HandleFunc("POST /assets/dynamic", r.requirePerm(auth.PermAssetsAssetCreate, r.handler.AssetCreateDynamic))
		mux.HandleFunc("POST /assets/dynamic/{id}", r.requirePerm(auth.PermAssetsAssetEdit, r.handler.AssetUpdateDynamic))

		// Events
		mux.HandleFunc("GET /events", r.requireAuth(r.handler.EventsList))

		// Sensors
		mux.HandleFunc("GET /sensors", r.requireAuth(r.handler.SensorsList))
		mux.HandleFunc("GET /sensors/new", r.requirePerm(auth.PermAssetsAssetCreate, r.handler.SensorNew))
		mux.HandleFunc("POST /sensors", r.requirePerm(auth.PermAssetsAssetCreate, r.handler.SensorCreate))
		mux.HandleFunc("GET /sensors/{id}", r.requireAuth(r.handler.SensorDetail))
		mux.HandleFunc("POST /sensors/{id}/delete", r.requirePerm(auth.PermAssetsAssetCreate, r.handler.SensorDelete))
		mux.HandleFunc("GET /sensors/{id}/bind", r.requirePerm(auth.PermAssetsAssetCreate, r.handler.SensorBindForm))
		mux.HandleFunc("POST /sensors/{id}/bind", r.requirePerm(auth.PermAssetsAssetCreate, r.handler.SensorBind))
		mux.HandleFunc("POST /sensors/{id}/unbind", r.requirePerm(auth.PermAssetsAssetCreate, r.handler.SensorUnbind))
		// Alerts
		mux.HandleFunc("GET /alerts", r.requireAuth(r.handler.AlertsList))
		mux.HandleFunc("GET /alerts/{id}", r.requireAuth(r.handler.AlertDetail))
		mux.HandleFunc("POST /alerts/{id}/acknowledge", r.requireAuth(r.handler.AlertAcknowledge))
		mux.HandleFunc("POST /alerts/{id}/resolve", r.requireAuth(r.handler.AlertResolve))
		// Map
		mux.HandleFunc("GET /map", r.requireAuth(r.handler.MapView))
	}

	// FSM Editor, Rules, Asset Types, master tables (SEAM_ENABLE_ADMIN=true)
	if r.config.EnableAdmin {
		// Rules Editor (Rete.js node graph)

		// FSM Editor (Evan Wallace canvas)
		mux.HandleFunc("GET /fsm/editor", r.requirePerm(auth.PermAssetsRuleCreate, r.handler.FSMEditorNew))
		mux.HandleFunc("GET /fsm/editor/{id}", r.requireAuth(r.handler.FSMEditorEdit))
		mux.HandleFunc("POST /fsm/export/png", r.requireAuthRawBody(r.handler.FSMExportPNG))

		// Rules
		mux.HandleFunc("GET /rules", r.requireAuth(r.handler.RulesList))
		mux.HandleFunc("GET /rules/new", r.requirePerm(auth.PermAssetsRuleCreate, r.handler.RuleNew))
		mux.HandleFunc("POST /rules", r.requirePerm(auth.PermAssetsRuleCreate, r.handler.RuleCreate))
		mux.HandleFunc("GET /rules/{id}", r.requireAuth(r.handler.RuleEdit))
		mux.HandleFunc("POST /rules/{id}", r.requirePerm(auth.PermAssetsRuleEdit, r.handler.RuleUpdate))

		// Asset Types
		mux.HandleFunc("GET /asset-types", r.requireAuth(r.handler.AssetTypesList))
		mux.HandleFunc("GET /asset-types/new", r.requireAuth(r.handler.AssetTypeNew))
		mux.HandleFunc("POST /asset-types", r.requireAuth(r.handler.AssetTypeCreate))
		mux.HandleFunc("GET /asset-types/{id}", r.requireAuth(r.handler.AssetTypeDetail))
		mux.HandleFunc("GET /asset-types/{id}/summary", r.requireAuth(r.handler.AssetTypeSummary))
		mux.HandleFunc("GET /asset-types/{id}/edit", r.requireAuth(r.handler.AssetTypeEdit))
		mux.HandleFunc("POST /asset-types/{id}", r.requireAuth(r.handler.AssetTypeUpdate))
		mux.HandleFunc("POST /asset-types/{id}/delete", r.requireAuth(r.handler.AssetTypeDelete))

		// Master tables — reference data managed by admins/managers
		mux.HandleFunc("GET /regions", r.requireAuth(r.handler.RegionesList))
		mux.HandleFunc("GET /regions/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.RegionNew))
		mux.HandleFunc("POST /regions", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.RegionCreate))
		mux.HandleFunc("GET /regions/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.RegionEdit))
		mux.HandleFunc("POST /regions/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.RegionUpdate))
		mux.HandleFunc("POST /regions/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.RegionDelete))

		mux.HandleFunc("GET /currencies", r.requireAuth(r.handler.MonedasList))
		mux.HandleFunc("GET /currencies/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MonedaNew))
		mux.HandleFunc("POST /currencies", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MonedaCreate))
		mux.HandleFunc("GET /currencies/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MonedaEdit))
		mux.HandleFunc("POST /currencies/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MonedaUpdate))
		mux.HandleFunc("POST /currencies/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MonedaDelete))

		mux.HandleFunc("GET /cost-centers", r.requireAuth(r.handler.CentrosCostoList))
		mux.HandleFunc("GET /cost-centers/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CentroCostoNew))
		mux.HandleFunc("POST /cost-centers", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CentroCostoCreate))
		mux.HandleFunc("GET /cost-centers/{id}/summary", r.requireAuth(r.handler.CentroCostoSummary))
		mux.HandleFunc("GET /cost-centers/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CentroCostoEdit))
		mux.HandleFunc("POST /cost-centers/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CentroCostoUpdate))
		mux.HandleFunc("POST /cost-centers/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CentroCostoDelete))

		mux.HandleFunc("GET /brands", r.requireAuth(r.handler.MarcasList))
		mux.HandleFunc("GET /brands/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MarcaNew))
		mux.HandleFunc("POST /brands", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MarcaCreate))
		mux.HandleFunc("GET /brands/{id}/summary", r.requireAuth(r.handler.MarcaSummary))
		mux.HandleFunc("GET /brands/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MarcaEdit))
		mux.HandleFunc("POST /brands/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MarcaUpdate))
		mux.HandleFunc("POST /brands/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.MarcaDelete))

		mux.HandleFunc("GET /custodians", r.requireAuth(r.handler.CustodiosList))
		mux.HandleFunc("GET /custodians/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CustodioNew))
		mux.HandleFunc("POST /custodians", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CustodioCreate))
		mux.HandleFunc("GET /custodians/{id}/summary", r.requireAuth(r.handler.CustodioSummary))
		mux.HandleFunc("GET /custodians/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CustodioEdit))
		mux.HandleFunc("POST /custodians/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CustodioUpdate))
		mux.HandleFunc("POST /custodians/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.CustodioDelete))

		mux.HandleFunc("GET /alert-types", r.requireAuth(r.handler.AlertasMasterList))
		mux.HandleFunc("GET /alert-types/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.AlertaMasterNew))
		mux.HandleFunc("POST /alert-types", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.AlertaMasterCreate))
		mux.HandleFunc("GET /alert-types/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.AlertaMasterEdit))
		mux.HandleFunc("POST /alert-types/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.AlertaMasterUpdate))
		mux.HandleFunc("POST /alert-types/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.AlertaMasterDelete))

		mux.HandleFunc("GET /properties", r.requireAuth(r.handler.PropDefsList))
		mux.HandleFunc("GET /properties/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.PropDefNew))
		mux.HandleFunc("POST /properties", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.PropDefCreate))
		mux.HandleFunc("GET /properties/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.PropDefEdit))
		mux.HandleFunc("POST /properties/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.PropDefUpdate))
		mux.HandleFunc("POST /properties/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.PropDefDelete))

		mux.HandleFunc("GET /models", r.requireAuth(r.handler.ModelosList))
		mux.HandleFunc("GET /models/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.ModeloNew))
		mux.HandleFunc("POST /models", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.ModeloCreate))
		mux.HandleFunc("GET /models/{id}/summary", r.requireAuth(r.handler.ModeloSummary))
		mux.HandleFunc("GET /models/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.ModeloEdit))
		mux.HandleFunc("POST /models/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.ModeloUpdate))
		mux.HandleFunc("POST /models/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.ModeloDelete))

		mux.HandleFunc("GET /locations", r.requireAuth(r.handler.UbicacionesList))
		mux.HandleFunc("GET /locations/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.UbicacionNew))
		mux.HandleFunc("POST /locations", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.UbicacionCreate))
		mux.HandleFunc("GET /locations/{id}/summary", r.requireAuth(r.handler.UbicacionSummary))
		mux.HandleFunc("GET /locations/{id}/edit", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.UbicacionEdit))
		mux.HandleFunc("POST /locations/{id}", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.UbicacionUpdate))
		mux.HandleFunc("POST /locations/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.UbicacionDelete))

		mux.HandleFunc("GET /properties/associations", r.requireAuth(r.handler.PropAssocsList))
		mux.HandleFunc("GET /properties/associations/new", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.PropAssocNew))
		mux.HandleFunc("POST /properties/associations", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.PropAssocCreate))
		mux.HandleFunc("POST /properties/associations/{id}/delete", r.requirePerm(auth.PermAssetsMasterEdit, r.handler.PropAssocDelete))
	}
}

// MountWorkOrdersRoutes registers the Work Orders playground module's
// routes (2026-08-04, T-67's follow-on) -- gated behind
// r.config.EnableWorkOrders, default false (see internal/config's own
// field doc comment for why). All routes use requireAuth only, no
// requirePerm -- the outer EnableWorkOrders gate is this playground's
// real safety boundary, not a granular per-action permission scheme,
// which T-11's own real scoping would need to design properly.
func (r *Router) MountWorkOrdersRoutes(mux *http.ServeMux) {
	if !r.config.EnableWorkOrders {
		return
	}
	mux.HandleFunc("GET /workorders", r.requireAuth(r.handler.WorkOrdersList))
	mux.HandleFunc("GET /workorders/new", r.requireAuth(r.handler.WorkOrderNew))
	mux.HandleFunc("POST /workorders", r.requireAuth(r.handler.WorkOrderCreate))
	mux.HandleFunc("GET /workorders/{id}", r.requireAuth(r.handler.WorkOrderDetail))
	mux.HandleFunc("POST /workorders/{id}/start", r.requireAuth(r.handler.WorkOrderStart))
	mux.HandleFunc("POST /workorders/{id}/complete", r.requireAuth(r.handler.WorkOrderComplete))
}

// MountIssuesRoutes registers the Issue Tracker module's routes
// (2026-08-05) -- simple and always-on, no feature flag: unlike Work
// Orders, nothing here needed the "playground, opt-in" framing.
// requireAuth only, matching Work Orders' own reasoning -- a granular
// permission scheme is real, later work if this module's scope grows,
// not needed for a simple, always-visible tracker.
func (r *Router) MountIssuesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /issues", r.requireAuth(r.handler.IssuesList))
	mux.HandleFunc("GET /issues/new", r.requireAuth(r.handler.IssueNew))
	mux.HandleFunc("POST /issues", r.requireAuth(r.handler.IssueCreate))
	mux.HandleFunc("GET /issues/{id}", r.requireAuth(r.handler.IssueDetail))
	mux.HandleFunc("POST /issues/{id}/toggle-status", r.requireAuth(r.handler.IssueToggleStatus))
}

// MountDocumentsRoutes registers the Documents module's routes -- just
// the dashboard landing page (GET /documents) -- onto mux.
//
// This is MOD-7's application of the same MountXRoutes pattern MOD-5/
// MOD-6 established, at the opposite end of the size spectrum: 1 route,
// not 13 or 98. Documents has no other views yet (DOCUMENTS_MODULE_
// SPEC.md v0.5 is a design document, T-04 to build it is still open),
// so there is nothing else to register -- MODULE_SPEC.md §7.2's own
// framing, "registers only the dashboard landing route." Unconditional,
// same reasoning as the System dashboard and Profile: a read-only
// landing page, not gated behind EnableAdmin/EnableOperational.
func (r *Router) MountDocumentsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /documents", r.requireAuth(r.handler.DocumentsDashboard))
}

// CombinedRouter combines API and UI routers.
type CombinedRouter struct {
	api    http.Handler
	ui     http.Handler
	static http.Handler
}

// NewCombinedRouter creates a router that serves both API and UI.
func NewCombinedRouter(api, ui http.Handler) *CombinedRouter {
	return &CombinedRouter{
		api:    api,
		ui:     ui,
		static: http.FileServer(http.Dir("web/static")),
	}
}

// ServeHTTP routes requests to API or UI based on path.
func (r *CombinedRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path

	// API routes
	if len(path) >= 8 && path[:8] == "/api/v1/" {
		r.api.ServeHTTP(w, req)
		return
	}

	// Static files
	if len(path) >= 8 && path[:8] == "/static/" {
		http.StripPrefix("/static/", r.static).ServeHTTP(w, req)
		return
	}

	// UI routes
	r.ui.ServeHTTP(w, req)
}
