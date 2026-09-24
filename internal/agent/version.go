package agent

// Version is stamped at build time by scripts/build.sh (-X .../agent.Version=v1.2.3).
// It is reported in every heartbeat so the hub can show which build is running.
var Version = "0.0.0-dev"
