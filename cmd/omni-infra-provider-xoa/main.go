// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package main runs the Xen Orchestra Omni infrastructure provider.
package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/siderolabs/omni/client/pkg/client"
	"github.com/siderolabs/omni/client/pkg/infra"
	"github.com/spf13/cobra"
	xoaclient "github.com/vatesfr/xenorchestra-go-sdk/client"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider"
	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/data"
	"github.com/kreove/omni-infra-provider-xoa/internal/pkg/provider/meta"
)

//go:embed data/icon.svg
var icon []byte

// version is stamped at build time with -ldflags "-X main.version=...".
// It stays "dev" for local builds.
var version = "dev"

var cfg struct {
	omniAPIEndpoint        string
	serviceAccountKey      string
	providerName           string
	providerDescription    string
	xoaEndpoint            string
	xoaToken               string
	xoaUsername            string
	xoaPassword            string
	xoaInsecure            bool
	imageFactoryBaseURL    string
	omniInsecureSkipVerify bool
}

var rootCmd = &cobra.Command{
	Use:          "omni-infra-provider-xoa",
	Short:        "Xen Orchestra Omni infrastructure provider",
	Long:         "Connects to Sidero Omni as an infrastructure provider and manages Talos VMs on XCP-ng through Xen Orchestra.",
	Version:      version,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		loggerConfig := zap.NewProductionConfig()
		logger, err := loggerConfig.Build(zap.AddStacktrace(zapcore.ErrorLevel))
		if err != nil {
			return fmt.Errorf("failed to create logger: %w", err)
		}

		if cfg.omniAPIEndpoint == "" {
			return fmt.Errorf("omni-api-endpoint is required")
		}

		if cfg.xoaEndpoint == "" {
			return fmt.Errorf("xoa-endpoint is required")
		}

		xoaConfig := xoaclient.Config{
			Url:                cfg.xoaEndpoint,
			InsecureSkipVerify: cfg.xoaInsecure,
		}

		switch {
		case cfg.xoaToken != "":
			xoaConfig.Token = cfg.xoaToken
		case cfg.xoaUsername != "" && cfg.xoaPassword != "":
			xoaConfig.Username = cfg.xoaUsername
			xoaConfig.Password = cfg.xoaPassword
		default:
			return fmt.Errorf("set XOA_TOKEN or both XOA_USERNAME and XOA_PASSWORD")
		}

		xoClientIface, err := xoaclient.NewClient(xoaConfig)
		if err != nil {
			return fmt.Errorf("failed to create Xen Orchestra client: %w", err)
		}

		// The provisioner needs the concrete client type, not just the
		// XOClient interface, to reach the raw JSON-RPC Call escape hatch
		// used for XO methods the SDK doesn't wrap (see image.go).
		xoClient, ok := xoClientIface.(*xoaclient.Client)
		if !ok {
			return fmt.Errorf("unexpected Xen Orchestra client implementation type %T", xoClientIface)
		}

		provisioner := provider.NewProvisioner(
			xoClient,
			cfg.imageFactoryBaseURL,
		)
		infrastructureProvider, err := infra.NewProvider(
			meta.ProviderID,
			provisioner,
			infra.ProviderConfig{
				Name:        cfg.providerName,
				Description: cfg.providerDescription,
				Icon:        base64.RawStdEncoding.EncodeToString(icon),
				Schema:      string(data.Schema),
			},
		)
		if err != nil {
			return fmt.Errorf("failed to create infrastructure provider: %w", err)
		}

		clientOptions := []client.Option{
			client.WithInsecureSkipTLSVerify(cfg.omniInsecureSkipVerify),
		}
		if cfg.serviceAccountKey != "" {
			clientOptions = append(clientOptions, client.WithServiceAccount(cfg.serviceAccountKey))
		}

		logger.Info(
			"starting Xen Orchestra infrastructure provider",
			zap.String("version", version),
			zap.String("provider_id", meta.ProviderID),
			zap.String("xoa_endpoint", cfg.xoaEndpoint),
			zap.String("image_factory_base_url", cfg.imageFactoryBaseURL),
		)

		return infrastructureProvider.Run(
			cmd.Context(),
			logger,
			infra.WithOmniEndpoint(cfg.omniAPIEndpoint),
			infra.WithClientOptions(clientOptions...),
			infra.WithEncodeRequestIDsIntoTokens(),
		)
	},
}

func main() {
	if err := app(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func app() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	defer cancel()

	return rootCmd.ExecuteContext(ctx)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func init() {
	rootCmd.Flags().StringVar(
		&cfg.omniAPIEndpoint,
		"omni-api-endpoint",
		os.Getenv("OMNI_ENDPOINT"),
		"Omni API endpoint (defaults to OMNI_ENDPOINT)",
	)
	rootCmd.Flags().StringVar(
		&meta.ProviderID,
		"id",
		meta.ProviderID,
		"provider ID registered in Omni",
	)
	rootCmd.Flags().StringVar(
		&cfg.serviceAccountKey,
		"omni-service-account-key",
		os.Getenv("OMNI_SERVICE_ACCOUNT_KEY"),
		"Omni service account key (defaults to OMNI_SERVICE_ACCOUNT_KEY)",
	)
	rootCmd.Flags().StringVar(&cfg.providerName, "provider-name", "Xen Orchestra", "provider name shown in Omni")
	rootCmd.Flags().StringVar(
		&cfg.providerDescription,
		"provider-description",
		"Xen Orchestra / XCP-ng infrastructure provider (alpha)",
		"provider description shown in Omni",
	)
	rootCmd.Flags().StringVar(
		&cfg.xoaEndpoint,
		"xoa-endpoint",
		firstNonEmpty(os.Getenv("XOA_ENDPOINT"), os.Getenv("XOA_HOST")),
		"Xen Orchestra base URL, e.g. wss://xoa.example.com (defaults to XOA_ENDPOINT, then XOA_HOST)",
	)
	rootCmd.Flags().StringVar(
		&cfg.imageFactoryBaseURL,
		"image-factory-base-url",
		firstNonEmpty(os.Getenv("TALOS_IMAGE_FACTORY_BASE_URL"), "https://factory.talos.dev"),
		"Talos Image Factory base URL",
	)
	rootCmd.Flags().StringVar(
		&cfg.xoaToken,
		"xoa-token",
		os.Getenv("XOA_TOKEN"),
		"Xen Orchestra API token (defaults to XOA_TOKEN)",
	)
	rootCmd.Flags().StringVar(
		&cfg.xoaUsername,
		"xoa-username",
		os.Getenv("XOA_USERNAME"),
		"Xen Orchestra username when token authentication is not used",
	)
	rootCmd.Flags().StringVar(
		&cfg.xoaPassword,
		"xoa-password",
		os.Getenv("XOA_PASSWORD"),
		"Xen Orchestra password when token authentication is not used",
	)
	rootCmd.Flags().BoolVar(
		&cfg.xoaInsecure,
		"xoa-insecure-skip-verify",
		false,
		"skip Xen Orchestra TLS certificate verification",
	)
	rootCmd.Flags().BoolVar(
		&cfg.omniInsecureSkipVerify,
		"insecure-skip-verify",
		false,
		"skip Omni TLS verification",
	)
}
