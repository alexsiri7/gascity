#!/usr/bin/env bash
# act2-provider-swap.sh — Act 2: Provider Swap (Local tmux → Docker containers)
#
# Demonstrates: Same lifecycle topology, different infrastructure.
# The audience just saw Act 1 end with a running lifecycle topology
# on local tmux. Now we show the same topology in Docker containers —
# same beads, same gc commands, different provider config.
#
# The presenter uncomments a [session] block in city.toml, saves, and
# the controller hot-reloads the session provider — agents gracefully
# stop on tmux and start in Docker containers. No restart needed.
# Beads, events, and mail share the filesystem — no sync needed.
#
# All agents are bash scripts — no Claude API calls.
#
# Usage:
#   ./act2-provider-swap.sh
#
# Env vars:
#   ACT2_TIMEOUT       — auto-teardown seconds (default: 120)
#   DEMO_CITY          — city directory (default: ~/demo-city)
#   GC_SRC             — gascity source tree (default: /data/projects/gascity)
#   GC_DOCKER_IMAGE    — base image with bash/git/tmux (default: gc-agent:latest)
#   EDITOR             — editor for city.toml edits (default: nano)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GC_SRC="${GC_SRC:-/data/projects/gascity}"
# shellcheck source=narrate.sh
source "$SCRIPT_DIR/narrate.sh"

DEMO_CITY="${DEMO_CITY:-$HOME/demo-city}"
DEMO_SESSION="gc-provider"
ACT2_TIMEOUT="${ACT2_TIMEOUT:-120}"
GC_DOCKER_IMAGE="${GC_DOCKER_IMAGE:-gc-agent:latest}"
EDIT="${EDITOR:-nano}"

# ── Preflight ─────────────────────────────────────────────────────────────

command -v gc >/dev/null 2>&1 || { echo "ERROR: gc not found in PATH" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "ERROR: docker not found in PATH" >&2; exit 1; }
command -v tmux >/dev/null 2>&1 || { echo "ERROR: tmux not found in PATH" >&2; exit 1; }

# ── Act 2 ─────────────────────────────────────────────────────────────────

narrate "Act 2: Provider Swap" --sub "Same lifecycle topology — local tmux → Docker containers"

echo "  Act 1 ended with lifecycle running on local tmux."
echo "  Now: same topology, same beads — running in Docker containers."
echo "  The only change: uncomment the [session] block in city.toml."
echo ""
pause

# ── Clean previous demo ──────────────────────────────────────────────────
# Stop ALL demo cities and sessions — a previous act may still be running.

for city in "$DEMO_CITY" "$HOME/demo-city" "$HOME/eks-demo"; do
    [ -d "$city" ] && (cd "$city" && gc stop 2>/dev/null) || true
done
for sess in gc-lifecycle gc-provider gc-eks; do
    tmux kill-session -t "$sess" 2>/dev/null || true
done

# Kill bd idle-monitors first (they respawn dolt servers if killed alone).
pkill -f "bd dolt idle-monitor" 2>/dev/null || true
# Kill any stale dolt servers that would block port 3307.
pkill -f "dolt sql-server" 2>/dev/null || true
# Wait for port 3307 to be released.
for i in $(seq 1 10); do
    ss -tlnp 2>/dev/null | grep -q ':3307 ' || break
    sleep 1
done

rm -rf "$DEMO_CITY"

# Stop any leftover gc Docker containers.
docker ps -q --filter label=gc.managed=true 2>/dev/null | xargs -r docker stop 2>/dev/null || true
docker ps -aq --filter label=gc.managed=true 2>/dev/null | xargs -r docker rm -f 2>/dev/null || true

# ── Set up city with lifecycle topology ──────────────────────────────────

# BEADS_DOLT_AUTO_START=0 prevents gc init / gc rig add from spawning their
# own dolt server + idle-monitor on port 3307. gc start will start the city
# dolt later with the correct data-dir.
export BEADS_DOLT_AUTO_START=0

step "Initializing city with lifecycle topology..."
gc init --from "$GC_SRC/examples/lifecycle" "$DEMO_CITY"

# Clone demo repo.
DEMO_REPO="$DEMO_CITY/demo-repo"
git clone -q https://github.com/julianknutsen/gc-demo-repo "$DEMO_REPO"

# Register rig (routes, hooks, topology config).
# Beads init fails (no dolt, auto-start disabled) — gc start handles it.
(cd "$DEMO_CITY" && gc rig add "$DEMO_REPO" --topology topologies/lifecycle) || true

# Remove partial metadata so gc start re-initializes on the city dolt.
rm -f "$DEMO_CITY/.beads/metadata.json"
rm -f "$DEMO_REPO/.beads/metadata.json"

unset BEADS_DOLT_AUTO_START

# Copy Docker session provider into the city.
mkdir -p "$DEMO_CITY/scripts"
cp "$GC_SRC/scripts/gc-session-docker" "$DEMO_CITY/scripts/"
chmod +x "$DEMO_CITY/scripts/gc-session-docker"

# Export image for the Docker provider script.
export GC_DOCKER_IMAGE

step "City ready with lifecycle topology + Docker provider script"

# ── Write city.toml with Docker session block commented out ──────────────

DEMO_REPO_ABS=$(cd "$DEMO_REPO" && pwd)

cat > "$DEMO_CITY/city.toml" <<EOF
# ── PROVIDER SWAP DEMO ──────────────────────────────────────────────────
#
# Current: Local providers (tmux sessions, bd beads, file events).
# To swap: Uncomment the [session] block below and save.
# The controller hot-reloads — agents move into Docker containers.
# ─────────────────────────────────────────────────────────────────────────

[workspace]
name = "demo-city"
start_command = "true"

[[rigs]]
name = "demo-repo"
path = "$DEMO_REPO_ABS"
includes = ["topologies/lifecycle"]

[daemon]
patrol_interval = "10s"

# ── SWAP: Uncomment below to move agents into Docker containers ──
# [session]
# provider = "exec:scripts/gc-session-docker"
EOF

# ── Show the city.toml ──────────────────────────────────────────────────

step "city.toml contents:"
echo ""
cat "$DEMO_CITY/city.toml"
echo ""

# ══════════════════════════════════════════════════════════════════════════
# SCREEN LAYOUT
# ══════════════════════════════════════════════════════════════════════════
#
# ┌─────────────────────────────┬─────────────────────────────┐
# │                             │                             │
# │  city.toml (editor)         │  gc status + docker ps      │
# │                             │                             │
# │  Presenter uncomments the   │  Cycles every 3s showing    │
# │  [session] block. Save.     │  both agent and container   │
# │                             │  status in one pane.        │
# │                             │                             │
# ├─────────────────────────────┼─────────────────────────────┤
# │                             │                             │
# │  gc events --follow         │  Controller (foreground)    │
# │                             │                             │
# │  Real-time event stream     │  gc start --foreground      │
# │  showing reconciliation.    │  with auto-restart loop.    │
# │                             │                             │
# └─────────────────────────────┴─────────────────────────────┘
#
# Left = cause (config edits, events). Right = effect (status, controller).
# ══════════════════════════════════════════════════════════════════════════

step "Creating 4-pane tmux layout..."
echo ""
echo "  ┌──────────────────────┬──────────────────────┐"
echo "  │ city.toml (editor)   │ Status + Docker      │"
echo "  ├──────────────────────┼──────────────────────┤"
echo "  │ gc events --follow   │ Controller           │"
echo "  └──────────────────────┴──────────────────────┘"
echo ""

PANE_EDITOR=$(tmux new-session -d -s "$DEMO_SESSION" -x 200 -y 50 -P -F "#{pane_id}")
PANE_STATUS=$(tmux split-window -h -t "$PANE_EDITOR" -P -F "#{pane_id}")
PANE_EVENTS=$(tmux split-window -v -t "$PANE_EDITOR" -P -F "#{pane_id}")
PANE_CTRL=$(tmux split-window -v -t "$PANE_STATUS" -P -F "#{pane_id}")

tmux select-pane -t "$PANE_EDITOR" -T "city.toml"
tmux select-pane -t "$PANE_STATUS" -T "Status + Docker"
tmux select-pane -t "$PANE_EVENTS" -T "Events"
tmux select-pane -t "$PANE_CTRL" -T "Controller"

tmux set-option -t "$DEMO_SESSION" pane-border-status top
tmux set-option -t "$DEMO_SESSION" pane-border-format "#{pane_title}"

# Pane top-right: gc status + docker ps (cycles every 3s).
tmux send-keys -t "$PANE_STATUS" \
    "cd $DEMO_CITY && watch -n3 'gc status 2>/dev/null || echo \"Starting...\"; echo; echo \"── Docker ──\"; docker ps --filter label=gc.managed=true --format \"table {{.Names}}\t{{.Status}}\t{{.Image}}\" 2>/dev/null || echo \"(no containers)\"'" C-m

# Pane bottom-left: Events stream.
tmux send-keys -t "$PANE_EVENTS" \
    "cd $DEMO_CITY && gc events --follow" C-m

# Pane bottom-right: Controller (foreground).
# The controller hot-reloads the session provider on config change —
# no Ctrl-C or restart needed. Edit config, save, watch agents move.
tmux send-keys -t "$PANE_CTRL" \
    "cd $DEMO_CITY && gc start --foreground" C-m

# Wait for gc start to bring up the dolt server + init beads.
step "Waiting for dolt server..."
for i in $(seq 1 30); do
    ss -tlnp 2>/dev/null | grep -q ':3307 ' && break
    sleep 1
done

# Wait for rig beads database to be initialized by gc start.
# Port 3307 means dolt is listening, but the rig database ("dr") is created
# by runBdInit during gc start's beads lifecycle — a few seconds after dolt.
step "Waiting for beads database..."
for i in $(seq 1 30); do
    (cd "$DEMO_REPO" && bd list >/dev/null 2>&1) && break
    sleep 1
done

# ── Seed beads (dolt is now running via gc start) ────────────────────────
# Seed 12 beads — enough for Phase 1 (local tmux, 5 polecats) AND Phase 2
# (Docker, 5 polecats). With patrol_interval=10s, one cycle claims up to 5.
step "Seeding work beads..."
(cd "$DEMO_REPO" && bd create "Add authentication module" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Fix parser edge case" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Update API documentation" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Implement rate limiting" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Add health check endpoint" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Create user profile page" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Add search functionality" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Implement caching layer" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Add logging middleware" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Create admin dashboard" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Add email notifications" --labels pool:demo-repo/polecat 2>/dev/null) || true
(cd "$DEMO_REPO" && bd create "Implement data export" --labels pool:demo-repo/polecat 2>/dev/null) || true
step "12 beads seeded"

# Pane top-left: Open city.toml in the editor.
tmux send-keys -t "$PANE_EDITOR" \
    "$EDIT $DEMO_CITY/city.toml" C-m

# Select the editor pane.
tmux select-pane -t "$PANE_EDITOR"

step "Demo ready. Attaching to tmux session..."
echo ""
echo "  INSTRUCTIONS:"
echo ""
echo "  Phase 1: City is running with LOCAL providers (tmux sessions)."
echo "           Status pane shows tmux-based agents."
echo "           Docker pane is empty — no containers yet."
echo ""
echo "  Phase 2: In the editor, uncomment the [session] block (2 lines)."
echo "           Save. The controller hot-reloads — no Ctrl-C needed."
echo "           Watch agents stop on tmux and start in Docker containers."
echo "           Check containers: docker ps --filter label=gc.managed=true"
echo ""
echo "  Same topology. Same beads. Different infrastructure."
echo ""
echo "  Detach: Ctrl-b d"
echo ""
pause "Press Enter to attach..."

tmux attach-session -t "$DEMO_SESSION"

# ── Teardown ────────────────────────────────────────────────────────────

(cd "$DEMO_CITY" && gc stop 2>/dev/null) || true

# Stop and remove Docker containers.
docker ps -q --filter label=gc.managed=true 2>/dev/null | xargs -r docker stop 2>/dev/null || true
docker ps -aq --filter label=gc.managed=true 2>/dev/null | xargs -r docker rm -f 2>/dev/null || true

tmux kill-session -t "$DEMO_SESSION" 2>/dev/null || true

# ── Done ────────────────────────────────────────────────────────────────

narrate "Act 2 Complete" --sub "Same topology, tmux → Docker — one config line"

echo "  Local providers:  tmux sessions, bd beads, file events"
echo "  Docker providers: containers with mounted work_dir, same beads"
echo "  The change:       uncomment [session] block, save — hot-reloaded"
echo ""
pause "Press Enter to continue to next act..."
