package main

import (
	"context"
	"fmt"
	"os"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/logging"
	"github.com/jerr3n/verde/executable/internal/project"
	"github.com/jerr3n/verde/executable/internal/session"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"
)

func serve(ctx context.Context, logger *zap.Logger, cfg *config.Cfg) error {
	tree, mnts, err := project.Load(cfg)
	if err != nil {
		return err
	}
	s := session.New(cfg, tree, mnts, logger)
	go s.Run(ctx)

	handleWs := func(c *gin.Context) {
		conn, err := websocket.Accept(c.Writer, c.Request, nil)
		if err != nil {
			logger.Warn("websocket upgrade", zap.Error(err))
			return
		}
		defer conn.CloseNow()

		if err := s.Handle(c.Request.Context(), conn); err != nil {
			logger.Warn("websocket connection", zap.Error(err))
		}
	}

	gin.DebugPrintRouteFunc = logging.RouteLogger(logger)

	r := gin.New()
	r.Use(logging.Middleware(logger), logging.Recovery(logger))

	r.GET("/", func(c *gin.Context) {
		if c.IsWebsocket() {
			handleWs(c)
			return
		}

		c.JSON(200, gin.H{
			"message": "Hello over HTTP!",
		})
	})

	r.GET("/ws", handleWs)

	logger.Info("listening", zap.String("addr", cfg.Addr()))
	return r.Run(cfg.Addr())
}

func main() {
	logger, err := logging.New(gin.IsDebugging())
	if err != nil {
		panic(err)
	}
	defer logger.Sync()
	var cmd = &cli.Command{
		Name:                  "verde",
		Description:           "multi-directional sync between roblox",
		EnableShellCompletion: true,
		Commands: []*cli.Command{
			{
				Name:  "init",
				Usage: "create a " + config.DefaultPath + " in the current folder",
				Action: func(ctx context.Context, command *cli.Command) error {
					if err := config.Init(config.DefaultPath); err != nil {
						return err
					}
					fmt.Printf("Created %s. Run `verde serve` to start syncing.\n", config.DefaultPath)
					return nil
				},
			},
			{
				Name: "serve",
				Action: func(ctx context.Context, command *cli.Command) error {
					cfg, err := config.Load(config.DefaultPath)
					if err != nil {
						return err
					}
					return serve(ctx, logger, cfg)
				},
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
