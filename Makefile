build/server:
	go build -o tunnel-server server/server.go

build/client:
	go build -o tunnel-client client/client.go

build/caddy:
	xcaddy build --with github.com/caddy-dns/cloudflare

up/caddy: build/caddy
	sudo -E ./caddy run --config ./Caddyfile
