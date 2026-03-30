package cli

import (
	"context"
	"fmt"
	"io"

	skrunchauth "github.com/sozercan/skrunch/internal/auth"
	"github.com/sozercan/skrunch/internal/githubscan"
	"github.com/sozercan/skrunch/internal/output"
	"github.com/spf13/cobra"
)

type options struct {
	Host            string
	Concurrency     int
	IncludeForks    bool
	IncludeArchived bool
	RepoMatch       string
	RepoLimit       int
	ShowOK          bool
	Debug           bool
}

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	opts := &options{}

	rootCmd := &cobra.Command{
		Use:           "skrunch",
		Short:         "Scan GitHub Actions workflow refs for missing, unreachable, or invalid refs",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.PersistentFlags().StringVar(&opts.Host, "host", "", "GitHub host to scan against (defaults to gh config or github.com)")
	rootCmd.PersistentFlags().IntVar(&opts.Concurrency, "concurrency", 8, "Maximum concurrent scans (repositories in org mode, files in repo/path mode)")
	rootCmd.PersistentFlags().BoolVar(&opts.IncludeForks, "include-forks", false, "Include fork repositories during org scans")
	rootCmd.PersistentFlags().BoolVar(&opts.IncludeArchived, "include-archived", false, "Include archived repositories during org scans")
	rootCmd.PersistentFlags().StringVar(&opts.RepoMatch, "match", "", "Only scan org repositories whose name or full name contains this case-insensitive substring")
	rootCmd.PersistentFlags().IntVar(&opts.RepoLimit, "limit", 0, "Maximum number of org repositories to scan after filters are applied")
	rootCmd.PersistentFlags().BoolVar(&opts.ShowOK, "show-ok", false, "Show refs that resolved successfully")
	rootCmd.PersistentFlags().BoolVar(&opts.Debug, "debug", false, "Show debug logs for scan stages and validation work")

	rootCmd.AddCommand(
		newRepoCmd(opts),
		newOrgCmd(opts),
		newPathCmd(opts),
	)

	return rootCmd
}

func newRepoCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "repo OWNER/REPO|URL",
		Short: "Scan a single repository URL or its .github/workflows directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommand(cmd, opts, func(ctx context.Context, scanner *githubscan.Scanner) (*githubscan.Report, error) {
				return scanner.ScanRepo(ctx, args[0])
			})
		},
	}
}

func newOrgCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "org ORG",
		Short: "Scan repositories in a GitHub organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommand(cmd, opts, func(ctx context.Context, scanner *githubscan.Scanner) (*githubscan.Report, error) {
				return scanner.ScanOrg(ctx, args[0])
			})
		},
	}
}

func newPathCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "path PATH",
		Short: "Scan a local path for workflow and composite action files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommand(cmd, opts, func(ctx context.Context, scanner *githubscan.Scanner) (*githubscan.Report, error) {
				return scanner.ScanPath(ctx, args[0])
			})
		},
	}
}

func runCommand(
	cmd *cobra.Command,
	opts *options,
	run func(context.Context, *githubscan.Scanner) (*githubscan.Report, error),
) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	resolvedAuth, err := skrunchauth.Resolve(opts.Host)
	if err != nil {
		return err
	}

	client, err := githubscan.NewClient(ctx, resolvedAuth)
	if err != nil {
		return err
	}

	if resolvedAuth.Token == "" {
		fmt.Fprintf(
			cmd.ErrOrStderr(),
			"using GitHub host %s without authentication; API rate limits may apply\n",
			resolvedAuth.Host,
		)
	} else {
		fmt.Fprintf(
			cmd.ErrOrStderr(),
			"using GitHub host %s (auth: %s)\n",
			resolvedAuth.Host,
			resolvedAuth.Source,
		)
	}

	var debugWriter io.Writer
	if opts.Debug {
		debugWriter = cmd.ErrOrStderr()
		fmt.Fprintf(
			cmd.ErrOrStderr(),
			"debug: scan options: concurrency=%d include_forks=%t include_archived=%t match=%q limit=%d show_ok=%t\n",
			opts.Concurrency,
			opts.IncludeForks,
			opts.IncludeArchived,
			opts.RepoMatch,
			opts.RepoLimit,
			opts.ShowOK,
		)
	}

	scanner := githubscan.NewScanner(client, githubscan.Options{
		Concurrency:     opts.Concurrency,
		IncludeForks:    opts.IncludeForks,
		IncludeArchived: opts.IncludeArchived,
		RepoMatch:       opts.RepoMatch,
		RepoLimit:       opts.RepoLimit,
		Progress:        cmd.ErrOrStderr(),
		DebugWriter:     debugWriter,
	})

	report, err := run(ctx, scanner)
	if err != nil {
		return err
	}

	if err := output.WriteText(cmd.OutOrStdout(), report, output.TextOptions{ShowOK: opts.ShowOK}); err != nil {
		return err
	}

	if report.HasFailures() {
		return &ExitError{Code: 1}
	}

	return nil
}
