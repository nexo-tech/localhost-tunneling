# Localhost Tunneling

A simple tunneling solution to expose local services to the internet.

## Configuration

The client can be configured using environment variables:

```bash
# Required: Server address (supports both IP and hostname)
TUNNEL_SERVER_IP=your.server.com  # or IP address like 192.168.1.100 or [2001:db8::1]
TUNNEL_SERVER_PORT=4000          # Optional, defaults to 4000 if not specified
```

### Examples:

```bash
# Using hostname
TUNNEL_SERVER_IP=my-tunnel-server.com
TUNNEL_SERVER_PORT=4000

# Using IPv4
TUNNEL_SERVER_IP=192.168.1.100
TUNNEL_SERVER_PORT=4000

# Using IPv6
TUNNEL_SERVER_IP=[2001:db8::1]
TUNNEL_SERVER_PORT=4000

# Using hostname without port (will use default port 4000)
TUNNEL_SERVER_IP=my-tunnel-server.com
```

## Usage

# localhost-tunneling

A basic client/server up to implement localhost tunneling


## .envrc example

```bash
export CF_API_TOKEN=

export DOMAIN=
export CADDY_ADMIN_API=localhost:2019
export TUNNEL_SERVER_PORT=4000
export CADDY_PROXY_PORT=3000

export EMAIL=
export PRODUCTION=true

# Client variables
export TUNNEL_SERVER_IP=
```
