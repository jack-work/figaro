package main

// As Theodore Roosevelt proclaimed, we shall "speak softly and carry a big stack"

import (
	"context"
	"encoding/json"
	"figaro/figaro"

	"figaro/forum"
	"figaro/logging"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	// if _, err := p.Run(); err != nil {
	// 	fmt.Printf("Alas, there's been an error: %v", err)
	// 	os.Exit(1)
	// }
	// return
	// establish root context
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(ctx.Err())

	// setup tracer and defer cleanup
	tp, err := logging.InitTracer(logging.WithServiceName("figaro"))
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			logging.EzPrint(fmt.Sprintf("Error shutting down tracer: %v", err))
		}
	}()

	// Define flag with default value "default_value"
	modePtr := flag.String("m", "ModelClaude3_7SonnetLatest", "Specify the model to use")

	// Parse flags
	flag.Parse()

	// init MCP
	servers, err := getServers()
	if err != nil {
		logging.EzPrint(err)
	}

	update := make(chan figaro.Event)
	defer close(update)

	// This is async tui
	forum.OpenForum(ctx, tp, update)

	// This blocks, once it's done, the tui is basically shot.
	figaro, cancel, err := figaro.SummonFigaro(ctx, tp, *servers, update)
	defer cancel(ctx.Err())

	if err != nil {
		return
	}

	// Use the flag value
	args := flag.Args()
	if len(args) > 0 {
		err := figaro.Request(args, modePtr)
		if err != nil {
			logging.EzPrint(err.Error())
		}
		cancel(nil)
		// <-update
		if err != nil {
			logging.EzPrint(err)
			return
		}
		return
	} else {
		logging.EzPrint("Nothing to say now.  Bye bye.")
		cancel(nil)
	}
}

func getServers() (*figaro.ServerRegistry, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	filePath := filepath.Join(homeDir, ".figaro", "servers.json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	// Unmarshal into struct and add the ID
	var config figaro.ServerRegistry
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}
