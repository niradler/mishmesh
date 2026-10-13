# UDP tunnels and agent connect (two-way routing)

Status: draft for review · 2026-10-13

## Why

A private network wants to host a VPN (WireGuard, OpenVPN-UDP) and let people reach it through mishmesh. VPNs speak UDP. mishmesh only carries `http`, `tcp` and `tls` today, and only in one direction: the server opens streams into an agent. UDP is rejected at agent config validation and at the gateway (`unsupported endpoint kind "udp"`).

Two things are needed:

1. **UDP as an endpoint kind.** Datagrams carried over the existing tunnel.
2. **Two-way routing.** Traffic can enter mishmesh at an agent, not only at a public server port. One agent exposes a service and another agent publishes it on its own network. mishmesh is the switch in the middle.

Together they turn mishmesh into a private, permissioned link between two networks: one org's agents, no public ports required.

## Vocabulary

| Term | Meaning |
|---|---|
| Origin side | Where the real service lives (the VPN server). Reached by an **origin agent** or, when the mishmesh server sits in the same network, by the server dialing it **directly** (the existing `proxy` method). |
| Entry side | Where clients send traffic. Either a **public port** on the mishmesh server or a **connect listener** on an **entry agent**. |
| Connect | An agent opening a stream *toward* the server, asking to reach an endpoint. The reverse of today's direction. |
| Flow | One UDP conversation, keyed by the client's source address on the entry side. One flow maps to one tunnel stream. |
| Private endpoint | An endpoint with no public listener. Reachable only through connect. |

## Flows

Two choices define every route:

- **Origin:** an origin agent, or the server dialing directly.
- **Entry:** a public server port, or a connect listener on an entry agent.

### Flow 1: platform exposes UDP publicly

```
VPN clients (internet)
      │ udp :40123
      ▼
┌──────────────────────┐   WSS (yamux stream, kind=udp)   ┌──────────────┐  udp   ┌────────────┐
│ mishmesh server      │ ───────────────────────────────▶ │ origin agent │ ─────▶ │ VPN :51820 │
│ public UDP listener  │ ◀─────────────────────────────── │  (agent A)   │ ◀───── │            │
└──────────────────────┘                                  └──────────────┘        └────────────┘
```

1. Agent A registers `{kind: udp, addr: 10.0.0.5:51820}`. The server claims a UDP port from its range and returns it in the binding (`udp://<host>:40123`).
2. A client packet arrives at `:40123`. The server looks up the flow for the client's address. On a miss it opens a stream to agent A with `StreamInit{kind: udp}`.
3. The server frames each datagram onto the stream. Agent A has one connected UDP socket per stream to `10.0.0.5:51820`, writes the payload and frames replies back.
4. The server unframes replies and sends them to the client address with `WriteTo`.
5. A flow with no traffic for `idle_timeout` (default 60s) closes its stream and socket.

Requirement: the server's host and load balancer must forward UDP on the port range.

### Flow 2: second agent exposes UDP on its network (agent connect)

```
VPN clients (branch / laptop)
      │ udp 127.0.0.1:51820 (or a LAN address)
      ▼
┌──────────────┐  WSS  ┌──────────────────────────┐  WSS / direct  ┌───────────────────────┐  udp  ┌────────────┐
│ entry agent  │ ────▶ │ mishmesh server          │ ─────────────▶ │ origin agent A        │ ────▶ │ VPN :51820 │
│  (agent B)   │ ◀──── │ authz + bridge streams   │ ◀───────────── │ or server dials direct│ ◀──── │            │
│ connect lsnr │       └──────────────────────────┘                └───────────────────────┘       └────────────┘
└──────────────┘
```

1. The VPN endpoint is registered as **private**: agent A with `private: true`, or a `proxy`-method endpoint the server dials directly. No public UDP port is claimed.
2. Agent B's config has a `connects` entry pointing at that endpoint. Agent B listens on UDP locally.
3. On a new flow, agent B opens a stream to the server: `StreamInit{endpoint_id, kind: udp, meta: {connect: "1"}}`.
4. The server accepts it, checks authorization (see [Authorization](#authorization)), then gets the origin stream:
   - origin agent: `agentA.OpenStream(ctx, endpointID, KindUDP, nil)`;
   - direct origin: the server opens a UDP socket to the target through `proxy.Guard`.
5. The server joins the two with `tunnel.Splice`. The frames pass through untouched, so the server never parses datagrams on this path.
6. Agent B unframes replies and sends them to the local client address.

Only WSS crosses the internet, so no public UDP is needed anywhere. Agent B and agent A can each sit behind NAT.

### Same network shortcut

If the mishmesh server is in the VPN's network, the origin agent is optional: register the endpoint with method `proxy` and target `udp://10.0.0.5:51820`. Both entry options still work. Flow 1 becomes "server public port → server dials VPN"; flow 2 becomes "agent B → server dials VPN".

### Route matrix

| Entry \ Origin | Origin agent | Server direct (`proxy`) |
|---|---|---|
| Public server port | Flow 1 | Public port → direct dial |
| Entry agent connect | Flow 2 (agent to agent) | Agent B → direct dial |

All four cells use the same flow table and framing. The difference is which stream ends get joined.

## Wire format

No change to the session, control channel or `StreamInit` layout. New kind constant `store.KindUDP = "udp"`.

On a `udp` stream, each datagram is one frame:

```
uint16 big-endian length | payload (length bytes)
```

- Max payload 65507 bytes. Larger datagrams are dropped and counted.
- Zero-length datagrams are valid and forwarded.
- Stream close = flow end. Either side may close on idle.

The framing lives in `internal/tunnel` (`WriteDatagram`, `ReadDatagram`, and a `DatagramConn` wrapper around a stream) so the server ingress, the agent and the bridge share it. It knows nothing about UDP sockets.

## Components

### `internal/tunnel`
- Datagram framing helpers above.
- `Session.AcceptData` is already symmetric. No changes.

### Agent (`internal/agent`)
- **Origin:** `proto: udp` passes validation. On an accepted stream of kind `udp`, open a UDP socket to the target with `net.DialUDP` and pump frames both ways until close or idle.
- **Entry:** a new `connects` config section. For each entry: listen with `net.ListenUDP`, keep a flow table keyed by source address, open one stream per flow with `sess.OpenData`, and drop flows on idle. TCP connects reuse the same section: accept, `OpenData`, `Splice`.

```yaml
tunnels:
  office-vpn:
    proto: udp
    addr: 10.0.0.5:51820
    private: true

connects:
  office-vpn:
    endpoint: office-vpn
    proto: udp
    listen: 127.0.0.1:51820
    idle_timeout: 120s
```

`listen` defaults to `127.0.0.1`. A non-loopback address (to serve a whole branch LAN) is allowed but must be set explicitly.

### Gateway (`internal/gateway`)
- Run an accept loop per agent session: `sess.AcceptData()` → hand to a `ConnectHandler` (an interface; the gateway stays a stream broker and does no routing itself).
- Registration accepts `kind: udp` and the `private` flag.

### Ingress (`internal/ingress`)
- `udp.go` next to `tcp.go`: `UDP.Open(endpointID, port)`, `Close`, `ListenCluster`, `Shutdown`, with the same port range and claims model as TCP.
- Port claims need a protocol namespace: TCP 40123 and UDP 40123 are different claims (`PortClaims` keyed by `proto:port`).
- A per-listener flow table, one goroutine per flow reading the stream, and a bounded send queue per flow. When a queue is full, packets are dropped, not blocked (normal UDP behavior).
- Binds to `BindHost` (`127.0.0.1` in dev), the same as TCP.

### Connect bridge (new, `internal/connect/bridge`)
- Implements `ConnectHandler`: authorize, resolve the origin (agent stream or direct dial), splice, meter.
- Direct UDP dial goes through `proxy.Guard` so the server-side SSRF rules apply to UDP as well. `Guard.Dial` gains a network parameter.

## Authorization

Connect is default deny.

- The entry agent's token resolves to an agent and org. The target endpoint must be in the same org.
- The endpoint policy gets a `connect` block:

```json
{ "connect": { "allow_agents": ["agt_123"], "allow_tags": ["branch"] } }
```

- No `connect` block means no agent may connect to it.
- A private endpoint with no `connect` allowance is reachable by nobody. Validation warns about this.
- Public UDP endpoints reuse `allow_cidrs`. Rate limiting applies per source IP, counted in new flows per second and packets per second.

## Limits and safety

- **Amplification:** a public UDP port can be abused to bounce traffic. The guards are per-IP flow caps, a per-endpoint `max_flows` (default 1024), the CIDR allow list, and replies sent only to addresses that already sent to us.
- **Quotas:** bytes are counted against the org like TCP. Each flow counts as a connection for connection quotas.
- **Head-of-line blocking:** datagrams travel inside TCP (WSS), so one loss on the tunnel stalls the flows behind it. This is fine for VPN admin access, DNS, syslog, games and IoT. Heavy VPN throughput or real-time media will degrade under loss. A future real datagram path (QUIC datagrams or WebTransport) could replace WSS without changing the framing above. Out of scope here.
- **MTU:** for WireGuard through mishmesh, the docs recommend `MTU = 1380` on the client.

## Observability

- Meter kind `udp`: bytes up and down.
- Metrics: `mishmesh_udp_flows_active{endpoint}`, `mishmesh_udp_datagrams_dropped_total{reason=oversize|queue_full|flow_limit}`, `mishmesh_connect_streams_total{result=ok|denied|no_origin}`.
- Logs: flow open and close at debug, connect denials at info with agent ID and endpoint ID.

## Testing

- Unit: framing round-trip, oversize, zero-length; flow table idle expiry; connect authz table (same org, other org, allow lists, no policy).
- E2E (`internal/e2e`):
  - `udp_test.go`: flow 1 echo through the public port, two concurrent clients, idle close.
  - `connect_test.go`: flow 2 agent-to-agent UDP echo and TCP echo; denied connect; private endpoint has no public port.
  - Direct origin: a `proxy`-method UDP endpoint, through the public port and through connect.
- Lab: a real WireGuard handshake and ping through flow 1 and flow 2.

## Phases

1. **UDP kind + agent origin + public port (flow 1).** Framing, agent UDP pump, `ingress/udp.go`, claims namespace, metering.
2. **Agent connect (flow 2).** Gateway accept loop, `ConnectHandler` bridge, `connects` config, private endpoints, connect policy. TCP connects included.
3. **Direct origin for UDP.** `proxy.Guard` UDP dial, `udp://` targets.
4. **Hardening.** Rate limits, flow caps, metrics, cluster mode for UDP ports.

## Open questions

1. Should connect allow-lists also accept **users** (a person's CLI token), or only agents for now? Recommendation: agents only in phase 2, users with identity work.
2. Cluster mode: a UDP packet can land on any node, and its flow state is node-local. Is sticky routing by source address at the load balancer acceptable, or must nodes share flow state? Recommendation: sticky routing (flows are short and cheap to rebuild).
3. Should `private: true` be the default for `udp` endpoints, so public UDP is opt-in? Recommendation: yes, because of the amplification risk.
