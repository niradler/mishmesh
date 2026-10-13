# UDP tunneling and agent-to-agent connects

Spec: `docs/superpowers/specs/2026-10-13-udp-and-agent-connect-design.md`
Branch: `work/network-layout` (uncommitted)

## State (2026-10-13)

Implemented and green under `make check` (race). The e2e tests passed 5 times in a row (`-count=5`).

### Flow 1: VPN → agent → public UDP port
- `internal/tunnel/datagram.go`: uint16 length-prefixed framing, `RelayDatagrams`, `DatagramListener` (a flow per client address, idle timeout, max flows), idle passed in `Meta["idle_timeout"]`
- `internal/ingress/udp.go`: public UDP port range, disabled in cluster mode
- gateway `registerPort` handles tcp and udp; `portsFor(kind)`
- agent `relayUDP` for `KindUDP` streams; CLI `mishmesh-agent udp <port>`

### Flow 2: VPN → agent B (or proxy) → mishmesh → agent A
- Private endpoints: `private: true` on tcp/udp tunnels means no public port, addressed by `policy.connect.name` (defaults to the tunnel name)
- `policy.connect.allow_agents`: agent names, ids, or `*`; empty means deny
- Agent `connects:` in the config opens local listeners; each stream carries `Meta["connect"]`
- Gateway `acceptConnects` loop → `internal/connect/bridge` checks org, kind and allow list, then calls origin `OpenStream` and splices
- Direct origin: a `method=proxy` endpoint with `udp://` targets plus a connect policy (proxy connector relays datagrams)

### Tests
- `internal/e2e/udp_connect_test.go`: public UDP port (4 concurrent clients), connect UDP, connect TCP, denied cases (not on list, other org, no policy, unknown), direct proxy origin
- `internal/connect/bridge/bridge_test.go`: authorize table and splice
- `internal/agent/config_test.go`: connects/private parsing and validation
- `internal/tunnel/datagram_test.go`

### Fixed along the way
- `mishmesh-agent start` did not pass `file.Connects` to the agent

## Open issues / gaps
- Cluster mode: UDP ingress is off; cross-pod connects should work through the relay (kind/meta agnostic) but are untested
- Health probes on `udp://` proxy targets always report healthy (UDP has no handshake)
- The control plane does not enforce unique connect names per org (the gateway checks this only for agent-registered endpoints)
- The HTTP ingress does not check endpoint kind (pre-existing)
- No UDP rate limiting or in-flight metering (usage is metered when a flow ends)
- Web UI has no view for private endpoints or connects yet

## Next
- Nir review → commit
- Optional: cluster e2e for cross-pod connects; UI for private endpoints/connects
