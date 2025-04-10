build/server:
	GOOS=linux GOARCH=amd64 go build -o tunnel-server server/server.go

build/client:
	go build -o tunnel-client client/client.go
