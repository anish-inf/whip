package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/context-labs/whip/internal/config"
	"github.com/context-labs/whip/internal/inferencenet"
)

// authInferenceNetCLI implements `whip auth inference-net …`: first-class
// Inference.net sign-in. The default path is a browser device-authorization
// login that provisions a machine API key automatically — no key handling.
// BYOK is supported via login --key / --env.
//
//	whip auth inference-net login [--key <apikey> | --env]
//	whip auth inference-net status
//	whip auth inference-net logout
//	whip auth inference-net key rotate
func authInferenceNetCLI(args []string) error {
	sub := "login"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "login":
		return inferenceNetLoginCLI(args)
	case "status":
		return inferenceNetStatusCLI()
	case "logout":
		return inferenceNetLogoutCLI()
	case "key":
		if len(args) > 0 && args[0] == "rotate" {
			return inferenceNetKeyRotateCLI()
		}
		return errors.New("usage: whip auth inference-net key rotate")
	default:
		return fmt.Errorf("unknown inference-net subcommand %q (login | status | logout | key rotate)", sub)
	}
}

func inferenceNetLoginCLI(args []string) error {
	fs := flag.NewFlagSet("auth inference-net login", flag.ContinueOnError)
	key := fs.String("key", "", "bring your own Inference.net API key instead of the browser login")
	envMode := fs.Bool("env", false, "store the BYOK key as apiKeyEnv: "+config.InferenceNetEnvVar+" instead of a literal in config.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	k := config.TrimKey(*key)
	if k == "" {
		k = config.TrimKey(os.Getenv(config.InferenceNetEnvVar))
	}
	// An explicit --key (even empty) or --env, or an env-provided key, takes the
	// BYOK path — so we never surprise the user with a browser flow they didn't
	// ask for (and a missing key errors instead of polling the device endpoint).
	keyFlagSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "key" {
			keyFlagSet = true
		}
	})
	if keyFlagSet || *envMode || k != "" {
		return inferenceNetBYOK(k, *envMode)
	}
	return inferenceNetDeviceLogin()
}

// inferenceNetBYOK validates a user-supplied key and persists it on the
// provider entry. Nothing is written until the key validates.
func inferenceNetBYOK(key string, envMode bool) error {
	if key == "" {
		return errors.New("no API key provided (set " + config.InferenceNetEnvVar + " or pass --key; get one at https://inference.net)")
	}
	fmt.Print("validating key against Inference.net… ")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := inferencenet.ValidateKey(ctx, key)
	cancel()
	if err != nil {
		fmt.Println("failed")
		return err
	}
	fmt.Println("ok")

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.UpsertInferenceNet(key, envMode)
	if err := cfg.Save(); err != nil {
		return err
	}
	if envMode && os.Getenv(config.InferenceNetEnvVar) == "" {
		offerShellExport(key)
	}
	fmt.Println("inference-net provider configured.")
	fmt.Println("  run `whip`, then /model to pick a model on inference-net.")
	return nil
}

// inferenceNetDeviceLogin runs the browser device flow, prompts for the
// team/project (creating a project on the spot if asked), mints a machine API
// key, and registers the provider — the user never touches a key.
func inferenceNetDeviceLogin() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	fmt.Println("Starting secure terminal authorization…")
	var opened bool
	auth, err := inferencenet.CompleteLogin(ctx, func(verificationURL, userCode string) {
		fmt.Println("\n  Approve this terminal in your browser:")
		fmt.Println("  " + verificationURL + "\n")
		fmt.Println("  Code: " + userCode + "\n")
		opened = openBrowser(verificationURL)
		if opened {
			fmt.Println("  Browser opened. Waiting for approval…")
		} else {
			fmt.Println("  Open the URL manually. Waiting for approval…")
		}
	}, cliChooser)
	if err != nil {
		return err
	}

	fmt.Println("Provisioning an API key for this machine…")
	if _, err := auth.EnsureMachineKey(ctx); err != nil {
		_ = inferencenet.ClearAuth()
		return err
	}
	if err := inferencenet.SaveAuth(auth); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// The machine key resolves via the ~/.whip/inference-net.json fallback, so
	// the provider entry carries no literal key.
	cfg.UpsertInferenceNet("", false)
	if err := cfg.Save(); err != nil {
		return err
	}

	fmt.Printf("\n✓ Signed in as %s\n", auth.UserEmail)
	fmt.Printf("  Project: %s (%s)\n", auth.ProjectName, auth.ProjectID)
	fmt.Printf("  Machine key: %s\n", auth.MachineKeyName)
	fmt.Println("  inference-net provider configured — run `whip`, then /model.")
	return nil
}

// cliChooser is the interactive picker for team/project selection. For a list
// it prints a numbered menu and reads a number (default 1); for a name prompt
// (no options) it reads free text.
func cliChooser(kind, title string, options []string) (string, error) {
	fmt.Println("\n" + title + ":")
	if len(options) == 0 {
		fmt.Print("  name: ")
		return readLine()
	}
	for i, o := range options {
		fmt.Printf("  %d) %s\n", i+1, o)
	}
	fmt.Printf("  pick [1-%d, default 1]: ", len(options))
	line, err := readLine()
	if err != nil {
		return "", err
	}
	if line == "" {
		return options[0], nil
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(options) {
		return "", fmt.Errorf("invalid choice %q", line)
	}
	return options[n-1], nil
}

func readLine() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func inferenceNetStatusCLI() error {
	auth, err := inferencenet.LoadAuth()
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p, ok := cfg.Providers[config.InferenceNetProvider]
	fmt.Println("Inference.net")
	if auth.SignedIn() {
		fmt.Println("  Account     " + auth.UserEmail)
		fmt.Println("  Project     " + auth.ProjectName + " (" + auth.ProjectID + ")")
	} else {
		fmt.Println("  Account     not signed in (whip auth inference-net login)")
	}
	if auth.HasMachineKey() {
		fmt.Println("  Machine key " + auth.MachineKeyName)
	}
	switch {
	case !ok:
		fmt.Println("  Provider    not configured")
	case p.APIKeyEnv != "":
		fmt.Println("  Provider    apiKeyEnv " + p.APIKeyEnv)
	case p.APIKey != "":
		fmt.Println("  Provider    literal apiKey")
	default:
		fmt.Println("  Provider    machine key (browser login)")
	}
	return nil
}

func inferenceNetLogoutCLI() error {
	auth, err := inferencenet.LoadAuth()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if auth.HasMachineKey() {
		if err := auth.ArchiveMachineKey(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "whip: could not disable the machine API key:", err)
		} else {
			fmt.Println("  Disabled this machine's API key.")
		}
	}
	if auth.SignedIn() {
		if err := inferencenet.SignOut(ctx, auth.SessionToken); err != nil {
			fmt.Fprintln(os.Stderr, "whip: the remote session could not be closed:", err)
		}
	}
	if err := inferencenet.ClearAuth(); err != nil {
		return err
	}
	fmt.Println("✓ Signed out of inference-net.")
	return nil
}

func inferenceNetKeyRotateCLI() error {
	auth, err := inferencenet.LoadAuth()
	if err != nil {
		return err
	}
	if !auth.SignedIn() {
		return errors.New("run `whip auth inference-net login` before rotating the machine API key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := auth.Rotate(ctx); err != nil {
		return err
	}
	if err := inferencenet.SaveAuth(auth); err != nil {
		return err
	}
	fmt.Println("✓ Rotated the machine API key: " + auth.MachineKeyName)
	return nil
}

// openBrowser opens url in the default browser; false when it can't.
func openBrowser(url string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(context.Background(), "open", url)
	case "windows":
		cmd = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.CommandContext(context.Background(), "xdg-open", url)
	}
	return cmd.Start() == nil
}
