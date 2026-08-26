# Colab interoperability boundary

SciAide does not provide the upstream WebSocket bridge, Cloudflare tunnel, notebook generator, remote Jupyter controller, or managed Google credentials described by the original package.

For a user-owned Colab workflow:

1. Create an ordinary notebook whose package versions and input hashes are explicit.
2. Upload only data the user has approved for Google Colab.
3. Run and monitor the notebook in the Google Colab interface.
4. Download code, logs, checkpoints, tables, and figures into the SciAide Workspace.
5. Verify returned files locally and register reusable outputs as Artifacts.

Do not call `colab_notebook`, `colab_connect`, or other nonexistent SciAide Tools. A future MCP integration must expose and audit its own real Tool contract before remote control can be claimed.
