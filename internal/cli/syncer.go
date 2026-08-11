package cli

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/ghsync"
	"github.com/scottlaird/todo/internal/service"
)

// flagInterval is shared with watch, which polls for the same reason.
const (
	flagRateLimitFloor = "rate-limit-floor"
	flagMaxFailures    = "max-failures"
)

func newSyncerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "syncer",
		Short: "Poll GitHub on a loop until interrupted",
		Long: "`todo sync github` on a timer. Quiet by design: a poll that changes\n" +
			"nothing prints nothing, and what did change is in the log, where\n" +
			"`todo watch` will show it. Only the exceptional is reported here —\n" +
			"slowing down for the rate limit, backing off, giving up.\n\n" +
			"Below --rate-limit-floor the remaining budget is spread across the time\n" +
			"until it resets, so polling slows exactly enough to last rather than by\n" +
			"an arbitrary amount. The interval is a floor as well as a default: this\n" +
			"never polls faster than asked.",
		Args: cobra.NoArgs,
		RunE: runSyncer,
	}

	f := cmd.Flags()
	f.Duration(flagInterval, ghsync.DefaultInterval, "gap between polls while the budget is healthy")
	f.Int(flagRateLimitFloor, ghsync.DefaultRateLimitFloor,
		"remaining rate limit below which polling is paced out")
	f.Int(flagMaxFailures, ghsync.DefaultMaxFailures,
		"consecutive failures to tolerate before giving up; 0 to keep trying")
	return cmd
}

func runSyncer(cmd *cobra.Command, _ []string) error {
	f := cmd.Flags()

	interval, err := f.GetDuration(flagInterval)
	if err != nil {
		return err
	}
	if interval <= 0 {
		return fmt.Errorf("--%s must be positive, not %s", flagInterval, interval)
	}
	floor, err := f.GetInt(flagRateLimitFloor)
	if err != nil {
		return err
	}
	maxFailures, err := f.GetInt(flagMaxFailures)
	if err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()

	// Ctrl-C stops the loop cleanly, the way `todo watch` does.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	syncer := &ghsync.Syncer{
		Store:          st,
		Client:         newFetcher(),
		Interval:       interval,
		RateLimitFloor: floor,
		MaxFailures:    maxFailures,
		Log:            cmd.ErrOrStderr(),
	}

	// Run through the service runner even for one service, so that adding a
	// web server or a log tailer later is a matter of listing them here.
	return service.Run(ctx, syncer, &schemaGuard{store: st})
}
