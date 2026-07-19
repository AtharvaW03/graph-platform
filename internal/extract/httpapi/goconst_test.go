package httpapi

import (
	"strings"
	"testing"
)

// TestGoConstantRoutes covers the org's dominant Go style: every path behind
// a constants.XxxRoute identifier, with nested Group() prefixes.
func TestGoConstantRoutes(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"constants/routes.go": `package constants

const (
	HealthRoute         = "/health"
	V1Route             = "/v1"
	MarginApiRoute      = "/margin"
	GetClientMarginRoute = "/client-margin"
	PledgeApiRoute      = "/pledge"
	UpdateLimitAPIRoute = "/update-limit"
)
`,
		"api/router.go": `package api

func Setup(router *gin.Engine) {
	router.GET(constants.HealthRoute, getHealthStatus)

	v1Group := router.Group(constants.V1Route, middleware.LoggingMiddleware())
	marginRoutes := v1Group.Group(constants.MarginApiRoute, middleware.AuthMWS2S())
	marginRoutes.GET(constants.GetClientMarginRoute, margin.GetClientMarginDetails)

	registerPledge(v1Group)
}

func registerPledge(r *gin.RouterGroup) {
	pledgeRoutes := r.Group(constants.PledgeApiRoute, middleware.AuthMWS2S())
	pledgeRoutes.POST(constants.UpdateLimitAPIRoute, v1Holding.AdminPledgeUpdateLimit)
}
`,
	})

	for _, want := range []string{
		"GET /health",
		// same-file group chain fully resolved
		"GET /v1/margin/client-margin",
		// cross-function group loses the parent prefix (documented
		// limitation) but the route itself must surface
		"POST /pledge/update-limit",
	} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
}

// TestGoConstantRoutesBareSegments: path constants that are bare segments
// with no leading slash and no route/path hint in the name must still
// resolve in route position. The group prefix lives in a different file
// than the registering function, so the paths surface partial (cross-file
// group resolution is a documented limitation); the routes must surface
// regardless.
func TestGoConstantRoutesBareSegments(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"constants/routes.go": `package constants

const (
	V2Group   = "widgets/v2"
	Detail    = "detail"
	Summary   = "summary"
	BatchEdit = "batch/edit"
)
`,
		"api/router.go": `package api

func Setup(router *gin.Engine) {
	v2Router := router.Group(constants.V2Group, authMW.Auth(), mw.Logging())
	v2.WidgetRoutesV2(v2Router)
}
`,
		"api/v2/routes.go": `package v2

func WidgetRoutesV2(group *gin.RouterGroup) {
	group.POST(constants.Detail, getDetail)
	group.GET(constants.Summary, getSummary)
	group.POST(constants.BatchEdit, postBatchEdit)
}
`,
	})

	for _, want := range []string{
		"POST /detail",
		"GET /summary",
		"POST /batch/edit",
	} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
}

// TestGoConstantRoutes_TypedRawAndConcatenated: custom-typed constants,
// raw-string literals, and concatenation chains (including a chain
// referencing another constant) all resolve to routes.
func TestGoConstantRoutes_TypedRawAndConcatenated(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"constants/routes.go": `package constants

type Route string

const (
	V1           = "/v1"
	DepositGroup = V1 + "/deposit"
	VerifyOTP    = DepositGroup + "/verify-otp"
	Profile      Route = "/user/profile"
	History      = ` + "`/transactions/history`" + ` // raw string
)
`,
		"api/router.go": `package api

func Setup(r *gin.Engine) {
	r.POST(constants.VerifyOTP, VerifyOTPController)
	r.PUT(constants.Profile, UpdateSelectedBankController)
	r.GET(constants.History, TransactionsHistoryController)
}
`,
	})
	for _, want := range []string{
		"POST /v1/deposit/verify-otp",
		"PUT /user/profile",
		"GET /transactions/history",
	} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
}

// TestGoWrappedRegistrations: formatter-wrapped registrations (arguments on
// their own lines) must match - wrapped identifier args, wrapped literal
// args, and a wrapped Group() definition whose prefix must still chain.
func TestGoWrappedRegistrations(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"constants/routes.go": `package constants

const CancelTransaction = "/transactions/cancel"
`,
		"api/router.go": `package api

func Setup(r *gin.Engine) {
	group := r.Group(
		"/v1",
	)
	group.POST(
		constants.CancelTransaction,
		middleware.Auth(),
		CancelTransactionController,
	)
	group.GET(
		"/deposit/charges",
		RatesAndChargesController,
	)
}
`,
	})
	for _, want := range []string{
		"POST /v1/transactions/cancel",
		"GET /v1/deposit/charges",
	} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
}

// TestGoEmptyLiteralRegistersOnGroupPath: `group.POST("", h)` is the gin
// idiom for "this route IS the group's path". It must emit the prefix as the
// route, never warn as an unresolved identifier.
func TestGoEmptyLiteralRegistersOnGroupPath(t *testing.T) {
	frag := runExtractFrag(t, map[string]string{
		"api/router.go": `package api

func Setup(r *gin.Engine) {
	orders := r.Group("/v5/orders")
	orders.POST("", createOrder)
	orders.PATCH("", modifyOrder)
	r.POST("", rootHandler)
}
`,
	})
	routes := map[string]bool{}
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			routes[n.Label] = true
		}
	}
	for _, want := range []string{"POST /v5/orders", "PATCH /v5/orders", "POST /"} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
	for _, w := range frag.Warnings {
		if strings.Contains(w, `"\"\""`) || strings.Contains(w, "dropped") {
			t.Errorf("empty-literal registration produced a warning: %q", w)
		}
	}
}

// TestGoEmptyConstantRegistersOnGroupPath: `orders.POST(constants.EMPTY, h)`
// where EMPTY = "" is the named-constant spelling of the group's-own-path
// idiom. Must emit the prefix, not warn.
func TestGoEmptyConstantRegistersOnGroupPath(t *testing.T) {
	frag := runExtractFrag(t, map[string]string{
		"constants/misc.go": `package constants

const EMPTY = ""
`,
		"api/router.go": `package api

func Setup(r *gin.Engine) {
	orders := r.Group("/v1/orders")
	orders.POST(constants.EMPTY, createOrder)
	orders.DELETE(constants.EMPTY, cancelOrder)
}
`,
	})
	routes := map[string]bool{}
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			routes[n.Label] = true
		}
	}
	for _, want := range []string{"POST /v1/orders", "DELETE /v1/orders"} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
	for _, w := range frag.Warnings {
		if strings.Contains(w, "EMPTY") {
			t.Errorf("EMPTY constant produced a warning: %q", w)
		}
	}
}

// TestGoLocalVariableRoutes: `endpoint := "/health-status"` locals used in
// route position must resolve, file-scoped so a same-named local in another
// file with a different value can't cross-contaminate.
func TestGoLocalVariableRoutes(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"api/actuator.go": `package api

func Setup(r *gin.Engine) {
	healthStatusEndpoint := "/health-status"
	pledgeSetupEndpoint := basePath + "/setup"
	r.GET(healthStatusEndpoint, healthHandler)
	r.POST(pledgeSetupEndpoint, setupHandler)
}

const basePath = "/v2/pledge"
`,
		"api/other.go": `package api

func Other(r *gin.Engine) {
	healthStatusEndpoint := "/completely-different"
	r.GET(healthStatusEndpoint, otherHandler)
}
`,
	})
	for _, want := range []string{
		"GET /health-status",
		"POST /v2/pledge/setup",
		"GET /completely-different",
	} {
		if !routes[want] {
			t.Errorf("missing route %q; got %v", want, routes)
		}
	}
	if routes["GET /health-status"] && !routes["GET /completely-different"] {
		t.Error("file-scoped locals leaked across files")
	}
}

// TestGoConstantRoutes_UnresolvedIsLoud: a route whose path identifier can't
// be resolved must surface as a fragment warning, never a silent drop.
func TestGoConstantRoutes_UnresolvedIsLoud(t *testing.T) {
	frag := runExtractFrag(t, map[string]string{
		"api/router.go": `package api

func Setup(r *gin.Engine) {
	r.POST(constants.BuiltAtRuntime, handler)
}
`,
	})
	found := false
	for _, w := range frag.Warnings {
		if strings.Contains(w, "BuiltAtRuntime") && strings.Contains(w, "dropped") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unresolved route identifier dropped silently; warnings: %v", frag.Warnings)
	}
}

// TestGoCommentedRoutesIgnored: a commented-out registration block (line
// comments and a /* */ block) must produce neither routes nor
// unresolved-identifier warnings. Live code with a trailing comment must
// still match.
func TestGoCommentedRoutesIgnored(t *testing.T) {
	frag := runExtractFrag(t, map[string]string{
		"api/actuator.go": `package api

// Legacy config-driven registration, kept for reference:
// 	var healthStatusEndpoint string
// 	healthStatusEndpoint = fmt.Sprintf("%v", healthStatusEndpointIface)
// 	r.GET(healthStatusEndpoint, healthHandler)
// 	r.POST("/commented-literal", oldHandler)

/*
r.POST("/block-commented", blockHandler)
group.GET(constants.DeadRoute, deadHandler)
*/

func Setup(r *gin.Engine) {
	r.GET("/live", liveHandler) // trailing comment must not hide this
}
`,
	})
	routes := map[string]bool{}
	for _, n := range frag.Nodes {
		if n.Type == "http_route" {
			routes[n.Label] = true
		}
	}
	if !routes["GET /live"] {
		t.Errorf("live route missing: %v", routes)
	}
	for label := range routes {
		if label != "GET /live" {
			t.Errorf("commented-out code produced route %q", label)
		}
	}
	if len(frag.Warnings) != 0 {
		t.Errorf("commented-out code produced warnings: %v", frag.Warnings)
	}
}

// TestGoConstantRoutesNoise: config-getter constants and unresolvable
// identifiers must not become routes.
func TestGoConstantRoutesNoise(t *testing.T) {
	routes := runExtract(t, map[string]string{
		"constants/configs.go": `package constants

const (
	ApplicationConfig = "application"
	McxFileBucket     = "mcx-bucket-name"
)
`,
		"business/job.go": `package business

func run() {
	bucket := configs.Get().GetStringD(constants.ApplicationConfig, constants.McxFileBucket, fallback)
	router.GET(constants.DoesNotExist, handler)
}
`,
	})
	if len(routes) != 0 {
		t.Errorf("noise extracted as routes: %v", routes)
	}
}
