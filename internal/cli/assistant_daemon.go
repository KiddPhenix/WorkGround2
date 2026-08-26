package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"workground2/internal/assistantdaemon"
	"workground2/internal/config"
)

func assistantCommand(args []string) int {
	if len(args) == 0 || args[0] != "daemon" {
		fmt.Fprintln(os.Stderr, "usage: WorkGround2 assistant daemon [--once] [--interval 30s] [--model name]")
		return 2
	}
	fs := flag.NewFlagSet("assistant daemon", flag.ContinueOnError)
	once := fs.Bool("once", false, "run one leader/schedule/execute/collect pass and exit")
	interval := fs.Duration("interval", 30*time.Second, "local daemon polling interval")
	model := fs.String("model", "", "provider name (default: config default_model)")
	store := fs.String("store", filepath.Join(config.MemoryUserDir(), "assistants"), "absolute Assistant store root")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	runtime, err := assistantdaemon.New(assistantdaemon.Options{StoreRoot: *store, Model: *model, Interval: *interval, Stderr: os.Stderr})
	if err != nil {
		fmt.Fprintln(os.Stderr, "assistant daemon:", err)
		return 1
	}
	defer runtime.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if *once {
		err = runtime.RunOnce(ctx)
	} else {
		err = runtime.Run(ctx)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "assistant daemon:", err)
		return 1
	}
	return 0
}
