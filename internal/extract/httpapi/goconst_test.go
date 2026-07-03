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
