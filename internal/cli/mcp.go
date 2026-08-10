package cli

import (
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scottlaird/todo/internal/mcp"
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
	st, err := openStore()
	if err != nil {
		return err
	}
	st.Close()

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	server := &mcp.Server{
		Name:    "todo",
		Version: version,
		Tools:   &mcpTools{db: dbPath, agent: actorName(agent)},
		Log:     cmd.ErrOrStderr(),
	}
	return server.Serve(ctx, cmd.InOrStdin(), cmd.OutOrStdout())
}
