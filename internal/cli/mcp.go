package cli

import (
	"context"
	"io"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/mcp"
	"github.com/scottlaird/todo/internal/service"
)

const flagAgent = "agent"

// version identifies this build to an MCP client. It is not a release
// number; nothing here is released.
const version = "0.1.0"

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the tool over MCP on stdin and stdout",
		Long: "For an agent, so it can read the queue and write to it without\n" +
			"shelling out. JSON-RPC over stdio: the process is spawned by whatever\n" +
			"is calling, and ends when its stdin does.\n\n" +
			"The tools are the CLI's own commands, derived from them rather than\n" +
			"written out again, so the two cannot drift. Left out are the ones an\n" +
			"agent has no business calling: `init`, which decides where state\n" +
			"lives, and `serve`, `syncer` and `watch`, which never return.\n\n" +
			"Every change is recorded as agent:<name>, taken from what the client\n" +
			"calls itself at startup and falling back to --agent. There is no way\n" +
			"to write as a person from here, which is the point.\n\n" +
			"Nothing is printed to stdout but the protocol; anything worth saying\n" +
			"goes to stderr.",
		Args: cobra.NoArgs,
		RunE: runMCP,
	}
	cmd.Flags().String(flagAgent, "mcp", "actor name for a client that does not identify itself")
	return cmd
}

func runMCP(cmd *cobra.Command, _ []string) error {
	agent, err := cmd.Flags().GetString(flagAgent)
	if err != nil {
		return err
	}

	// Fail here rather than on the first tool call: a server that cannot
	// reach its database has nothing to offer, and saying so at startup is
	// what the caller can act on.
	//
	// The handle is kept open for the schema guard. Tool calls do not use it —
	// each one runs the command tree, which opens its own.
	st, err := openStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()

	// Resolved once here rather than read per call: the tool tree gets the
	// path handed to it, so nothing it does depends on a flag this process
	// might parse again.
	dbPath, err := dbPathFrom(cmd)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	server := &mcp.Server{
		Name:    "todo",
		Version: version,
		Tools:   &mcpTools{db: dbPath, agent: actorName(agent)},
		Log:     cmd.ErrOrStderr(),
	}
	return service.Run(ctx,
		&mcpService{server: server, in: cmd.InOrStdin(), out: cmd.OutOrStdout()},
		&schemaGuard{store: st})
}

// mcpService is the protocol loop as a service, so it can be run beside the
// schema guard.
type mcpService struct {
	server *mcp.Server
	in     io.Reader
	out    io.Writer
}

func (m *mcpService) Name() string { return "mcp" }

// Run returns as soon as the context is cancelled, rather than waiting for
// Serve to notice.
//
// Serve blocks reading stdin and there is no portable way to interrupt that,
// so on cancellation it is left where it is: the process is on its way out,
// and the goroutine goes with it. Nothing is served in the meantime, because
// Serve checks the context before dispatching anything it does read.
func (m *mcpService) Run(ctx context.Context) error {
	served := make(chan error, 1)
	go func() { served <- m.server.Serve(ctx, m.in, m.out) }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
		return nil
	}
}
