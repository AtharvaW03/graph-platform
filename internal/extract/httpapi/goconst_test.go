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

// TestGoConstantRoutesBareSegments covers a common convention that a stricter
// constant filter used to miss: path constants are bare segments with no
// leading slash and plain names that contain no route/path/endpoint hint.
// When such a constant is used in route position it must still resolve -
// otherwise the route is dropped entirely. Here the group prefix is defined
// in a different file than the function that registers the routes, so the
// paths surface partial (cross-file group resolution is a separate, documented
// limitation); the point of this test is that the routes surface at all.
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

// TestGoConstantRoutes_TypedRawAndConcatenated covers the declaration shapes
// that previously caused routes to vanish entirely (the us-funds P0):
// custom-typed constants, raw-string literals, and concatenation chains
// (including a chain referencing another constant).
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
// their own lines) previously never matched any per-line regex and vanished.
// Covers wrapped identifier args, wrapped literal args, and a wrapped
// Group() definition whose prefix must still chain.
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
