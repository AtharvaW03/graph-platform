package httpapi

import "testing"

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
