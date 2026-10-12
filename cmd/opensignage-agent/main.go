// Command opensignage-agent is the OpenSignage client service that runs on every
// player: server link, content sync, local schedules, kiosk browser, client WebGUI.
//
// Usage:
//
//	opensignage-agent [run] [-config /etc/opensignage/agent.env]
//	opensignage-agent webgui-password [-config …] [-must-change]   (password on stdin)
//	opensignage-agent screenshot [-config …] -o shot.jpg           (CDP diagnostics)
//	opensignage-agent rollback [-config …]                         (systemd OnFailure)
//	opensignage-agent version
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image/jpeg"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"opensignage/internal/agent"
	"opensignage/internal/agent/config"
	"opensignage/internal/agent/kiosk"
	"opensignage/internal/agent/state"
	"opensignage/internal/agent/updater"
	"opensignage/internal/agent/webgui"
	"opensignage/internal/common/logging"
	"opensignage/internal/common/version"
)

func main() {
	cmd, args := "run", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run":
		err = run(args)
	case "webgui-password":
		err = webguiPassword(args)
	case "screenshot":
		err = screenshot(args)
	case "rollback":
		err = rollback(args)
	case "version":
		fmt.Printf("opensignage-agent %s (%s, protocol %d)\n", version.Version, version.Commit, version.Protocol)
	default:
		err = fmt.Errorf("unknown command %q (run, webgui-password, screenshot, rollback, version)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "opensignage-agent:", err)
		os.Exit(1)
	}
}

func loadConfig(fs *flag.FlagSet, args []string) (*config.Config, error) {
	file := fs.String("config", config.DefaultFile(), "configuration file")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	cfg, err := config.Load(*file)
	if err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func run(args []string) error {
	cfg, err := loadConfig(flag.NewFlagSet("run", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	_, logs, err := logging.Setup(filepath.Join(cfg.DataDir, "logs"), "agent", cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("logging: %w", err)
	}
	defer logs.Close()
	for _, w := range cfg.Warnings {
		slog.Warn("configuration: " + w)
	}
	if cfg.Chromium == "" {
		slog.Warn("no chromium binary found (install chromium or set OS_CHROMIUM)")
	}
	a, err := agent.New(cfg, agent.Options{Logs: logs})
	if err != nil {
		slog.Error("agent start failed", "err", err)
		return err
	}
	web, err := webgui.New(cfg.WebGUIPort, filepath.Join(cfg.DataDir, "tls"), a.Store(), a.I18n(), a)
	if err != nil {
		slog.Error("client WebGUI start failed", "err", err)
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx, web); err != nil {
		slog.Error("agent stopped with error", "err", err)
		return err
	}
	slog.Info("agent stopped")
	return nil
}

// rollback is run by opensignage-agent-rollback.service (OnFailure of the agent unit)
// with the PREVIOUS binary when the new version keeps crashing: it points `current`
// back at the release the update came from. systemd then starts the agent again.
func rollback(args []string) error {
	cfg, err := loadConfig(flag.NewFlagSet("rollback", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	installDir := cfg.InstallDir
	if installDir == "" {
		return errors.New("not running from an installed release (set OS_INSTALL_DIR)")
	}
	msg, err := updater.Rollback(cfg.DataDir, installDir)
	if err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}
	fmt.Println(msg)
	return nil
}

// webguiPassword sets the local client WebGUI password from stdin (installer option
// --webgui-password). Run it as the service user so file ownership stays correct.
func webguiPassword(args []string) error {
	fs := flag.NewFlagSet("webgui-password", flag.ExitOnError)
	mustChange := fs.Bool("must-change", false, "require a change at the next login")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("password expected on stdin")
	}
	pw := strings.TrimRight(line, "\r\n")
	st, err := state.New(cfg.DataDir)
	if err != nil {
		return err
	}
	if err := webgui.SetPassword(st, pw, *mustChange); err != nil {
		return fmt.Errorf("password rejected: %w", err)
	}
	fmt.Println("client WebGUI password set")
	return nil
}

// screenshot captures the running player through the DevTools endpoint and reports
// whether the image has visible content (hardware tests).
func screenshot(args []string) error {
	fs := flag.NewFlagSet("screenshot", flag.ExitOnError)
	out := fs.String("o", "screenshot.jpg", "output file")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	dir := filepath.Join(cfg.DataDir, "chromium")
	port, err := kiosk.ReadDevToolsPort(dir)
	if err != nil {
		return fmt.Errorf("player not running: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u, err := kiosk.PageWebSocketURL(ctx, port, fmt.Sprintf("http://127.0.0.1:%d/", cfg.PlayerPort))
	if err != nil {
		return err
	}
	c, err := kiosk.DialCDP(ctx, u)
	if err != nil {
		return err
	}
	defer c.Close()
	img, err := c.Screenshot(ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, img, 0o644); err != nil {
		return err
	}
	decoded, err := jpeg.Decode(bytes.NewReader(img))
	if err != nil {
		return fmt.Errorf("screenshot is not a valid JPEG: %w", err)
	}
	b := decoded.Bounds()
	var sum, sumSq float64
	var n float64
	bright := 0
	for y := b.Min.Y; y < b.Max.Y; y += 2 {
		for x := b.Min.X; x < b.Max.X; x += 2 {
			r, g, bl, _ := decoded.At(x, y).RGBA()
			l := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257
			sum += l
			sumSq += l * l
			n++
			if l > 40 {
				bright++
			}
		}
	}
	mean := sum / n
	std := 0.0
	if v := sumSq/n - mean*mean; v > 0 {
		std = math.Sqrt(v)
	}
	verdict := "content visible"
	if std < 2 || mean < 1 {
		verdict = "BLANK (uniform image)"
	}
	fmt.Printf("%s: %dx%d, %d bytes, mean luma %.1f, stddev %.1f, bright pixels %.1f %% → %s\n",
		*out, b.Dx(), b.Dy(), len(img), mean, std, float64(bright)/n*100, verdict)
	if strings.HasPrefix(verdict, "BLANK") {
		return errors.New("screenshot is blank")
	}
	return nil
}
