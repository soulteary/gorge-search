package httpapi

import (
	"net/http"

	"github.com/soulteary/gorge-search/internal/engine"

	"github.com/labstack/echo/v4"
)

type Deps struct {
	Engine *engine.SearchEngine
	Token  string
}

type apiResponse struct {
	Data  any       `json:"data,omitempty"`
	Error *apiError `json:"error,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func RegisterRoutes(e *echo.Echo, deps *Deps) {
	e.GET("/", healthPing())
	e.GET("/healthz", healthPing())

	g := e.Group("/api/search")
	g.Use(tokenAuth(deps))

	g.POST("/index", indexDocument(deps))
	g.POST("/query", searchQuery(deps))
	g.POST("/init", initIndex(deps))
	g.GET("/exists", indexExists(deps))
	g.GET("/stats", indexStats(deps))
	g.POST("/sane", indexIsSane(deps))
	g.GET("/backends", listBackends(deps))
}

func tokenAuth(deps *Deps) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if deps.Token == "" {
				return next(c)
			}
			token := c.Request().Header.Get("X-Service-Token")
			if token == "" {
				token = c.QueryParam("token")
			}
			if token == "" || token != deps.Token {
				return c.JSON(http.StatusUnauthorized, &apiResponse{
					Error: &apiError{Code: "ERR_UNAUTHORIZED", Message: "missing or invalid service token"},
				})
			}
			return next(c)
		}
	}
}

func healthPing() echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}
}

func respondOK(c echo.Context, data any) error {
	return c.JSON(http.StatusOK, &apiResponse{Data: data})
}

func respondErr(c echo.Context, status int, code, msg string) error {
	return c.JSON(status, &apiResponse{
		Error: &apiError{Code: code, Message: msg},
	})
}

func indexDocument(deps *Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		var doc engine.Document
		if err := c.Bind(&doc); err != nil {
			return respondErr(c, http.StatusBadRequest, "ERR_BAD_REQUEST", err.Error())
		}
		if doc.PHID == "" {
			return respondErr(c, http.StatusBadRequest, "ERR_BAD_REQUEST", "phid is required")
		}
		if doc.Type == "" {
			return respondErr(c, http.StatusBadRequest, "ERR_BAD_REQUEST", "type is required")
		}

		if err := deps.Engine.IndexDocument(&doc); err != nil {
			return respondErr(c, http.StatusBadGateway, "ERR_INDEX_FAILED", err.Error())
		}

		return respondOK(c, map[string]string{"phid": doc.PHID, "status": "indexed"})
	}
}

func searchQuery(deps *Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		var q engine.SearchQuery
		if err := c.Bind(&q); err != nil {
			return respondErr(c, http.StatusBadRequest, "ERR_BAD_REQUEST", err.Error())
		}

		phids, err := deps.Engine.Search(&q)
		if err != nil {
			return respondErr(c, http.StatusBadGateway, "ERR_SEARCH_FAILED", err.Error())
		}

		return respondOK(c, map[string]any{
			"phids": phids,
			"count": len(phids),
		})
	}
}

type initRequest struct {
	DocTypes []string `json:"docTypes"`
}

func initIndex(deps *Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		var req initRequest
		if err := c.Bind(&req); err != nil {
			return respondErr(c, http.StatusBadRequest, "ERR_BAD_REQUEST", err.Error())
		}
		if len(req.DocTypes) == 0 {
			return respondErr(c, http.StatusBadRequest, "ERR_BAD_REQUEST", "docTypes is required")
		}

		if err := deps.Engine.InitIndex(req.DocTypes); err != nil {
			return respondErr(c, http.StatusBadGateway, "ERR_INIT_FAILED", err.Error())
		}

		return respondOK(c, map[string]string{"status": "initialized"})
	}
}

func indexExists(deps *Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		exists, err := deps.Engine.IndexExists()
		if err != nil {
			return respondErr(c, http.StatusBadGateway, "ERR_CHECK_FAILED", err.Error())
		}
		return respondOK(c, map[string]bool{"exists": exists})
	}
}

func indexStats(deps *Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		stats, err := deps.Engine.IndexStats()
		if err != nil {
			return respondErr(c, http.StatusBadGateway, "ERR_STATS_FAILED", err.Error())
		}
		return respondOK(c, stats)
	}
}

type saneRequest struct {
	DocTypes []string `json:"docTypes"`
}

func indexIsSane(deps *Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		var req saneRequest
		if err := c.Bind(&req); err != nil {
			return respondErr(c, http.StatusBadRequest, "ERR_BAD_REQUEST", err.Error())
		}

		sane, err := deps.Engine.IndexIsSane(req.DocTypes)
		if err != nil {
			return respondErr(c, http.StatusBadGateway, "ERR_CHECK_FAILED", err.Error())
		}
		return respondOK(c, map[string]bool{"sane": sane})
	}
}

func listBackends(deps *Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondOK(c, deps.Engine.BackendInfo())
	}
}
