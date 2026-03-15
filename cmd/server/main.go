package main

import (
	"fmt"
	"log"
	"os"

	"github.com/soulteary/gorge-search/internal/config"
	"github.com/soulteary/gorge-search/internal/engine"
	"github.com/soulteary/gorge-search/internal/httpapi"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	var cfg *config.Config

	if path := os.Getenv("SEARCH_CONFIG_FILE"); path != "" {
		var err error
		cfg, err = config.LoadFromFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to load config from %s: %v\n", path, err)
			os.Exit(1)
		}
	} else {
		cfg = config.LoadFromEnv()
	}

	se := engine.New(cfg.Backends)

	if !se.HasBackends() {
		log.Println("warning: no search backends configured; search operations will fail until backends are added")
	}

	e := echo.New()
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus: true, LogURI: true, LogMethod: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			log.Printf("%s %s %d\n", v.Method, v.URI, v.Status)
			return nil
		},
	}))
	e.Use(middleware.Recover())

	httpapi.RegisterRoutes(e, &httpapi.Deps{
		Engine: se,
		Token:  cfg.ServiceToken,
	})

	e.Logger.Fatal(e.Start(cfg.ListenAddr))
}
