// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/jerr3n/verde/executable/internal/build"
	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/logging"
	"github.com/jerr3n/verde/executable/internal/project"
	"github.com/jerr3n/verde/executable/internal/session"
	"github.com/jerr3n/verde/executable/internal/watch"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"
)

// serve loads the project and starts the sync session and the disk watcher.
// It then accepts WebSocket connections on cfg.Addr() and blocks until the HTTP server stops.
func serve(ctx context.Context, logger *zap.Logger, cfg *config.Cfg) error {
	tree, mnts, err := project.Load(cfg)
	if err != nil {
		return err
	}
	s := session.New(cfg, tree, mnts, logger)
	go s.Run(ctx)
	go func() {
		if err := watch.Run(ctx, cfg, s.DiskChanged, logger); err != nil {
			logger.Error("watcher stopped", zap.Error(err))
		}
	}()

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

	// A client can connect at "/" or at "/ws". A plain HTTP request to "/" gets a JSON reply.
	r.GET("/", func(c *gin.Context) {
		if c.IsWebsocket() {
			handleWs(c)
			return
		}
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
		Description:           "Multi-directional sync between Roblox and Disk.",
		Usage:                 "Multi-directional sync between Roblox and Disk.",
		EnableShellCompletion: true,
		Commands: []*cli.Command{
			{
				Name:  "init",
				Usage: "create a " + config.DefaultPath + " and its sync folders in the current folder",
				Action: func(ctx context.Context, command *cli.Command) error {
					if err := config.Init(config.DefaultPath); err != nil {
						return err
					}
					fmt.Printf("Created %s and its sync folders. Run `verde serve` to start syncing.\n", config.DefaultPath)
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
			{
				Name:  "build",
				Usage: "build the project's files into a place or model file",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "output",
						Aliases:  []string{"o"},
						Usage:    "where to write it: game.rbxlx for a place, or Car.rbxmx for a model",
						Required: true,
					},
					&cli.StringFlag{
						Name:    "instance",
						Aliases: []string{"i"},
						Usage:   "what a model holds, such as Workspace/Car",
					},
				},
				Action: func(ctx context.Context, command *cli.Command) error {
					cfg, err := config.Load(config.DefaultPath)
					if err != nil {
						return err
					}
					t, mounts, err := project.Load(cfg)
					if err != nil {
						return err
					}
					output := command.String("output")
					result, err := build.WriteFile(output, t, mounts, command.String("instance"))
					if err != nil {
						return err
					}
					for _, warning := range result.Warnings {
						fmt.Fprintln(os.Stderr, "warning:", warning)
					}
					fmt.Printf("Built %s with %d instances.\n", output, result.Instances)
					return nil
				},
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
