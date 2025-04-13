# Localhost Tunneling

A simple tunneling solution to expose local services to the internet.

## Configuration

### Server Configuration
```bash
# Required: Domain configuration
export DOMAIN=yourdomain.com
export TUNNEL_SERVER_DOMAIN_NAME=tunnel.yourdomain.com  # Subdomain for tunnel server
export CADDY_ADMIN_API=localhost:2019
export CADDY_PROXY_PORT=3000

# Cloudflare configuration
export CF_API_TOKEN=your_cloudflare_token
```

### Client Configuration
```bash
# Required: Server address (supports both IP and hostname)
export TUNNEL_SERVER_IP=tunnel.yourdomain.com  # Must match TUNNEL_SERVER_DOMAIN_NAME
export TUNNEL_SERVER_PORT=443                  # Default port for HTTPS/WSS
```

### Examples:

```bash
# Server configuration
export DOMAIN=example.com
export TUNNEL_SERVER_DOMAIN_NAME=tunnel.example.com
export CF_API_TOKEN=your_token
export EMAIL=admin@example.com

# Client configuration
export TUNNEL_SERVER_IP=tunnel.example.com
export TUNNEL_SERVER_PORT=443
```

## Usage

1. Start the server:
```bash
go run server/server.go
```

2. Start the client:
```bash
go run client/client.go <local-port>
```

## Features

- Secure WebSocket-based tunneling
- Automatic TLS with Let's Encrypt
- Cloudflare integration for DNS and CDN
- Structured logging for easy debugging
