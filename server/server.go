package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/hashicorp/yamux"
)

var (
	portMutex   sync.Mutex
	nextPort    = 10000
	activePorts = make(map[int]bool)
	hostMap     = make(map[string]int)
)

func main() {
	// Start control server
	go startControlServer(4000)

	// Start HTTP server for Caddy
	http.HandleFunc("/", handleHTTPRequest)
	log.Fatal(http.ListenAndServe(":3000", nil))
}

func startControlServer(port int) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Fatal(err)
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println("Accept error:", err)
			continue
		}
		go handleClient(conn)
	}
}

func handleClient(conn net.Conn) {
	defer conn.Close()

	// Yamux session for multiplexing
	session, err := yamux.Server(conn, nil)
	if err != nil {
		log.Println("Yamux error:", err)
		return
	}

	// Get local port from client
	buf := make([]byte, 10)
	_, err = conn.Read(buf)
	if err != nil {
		log.Println("Read error:", err)
		return
	}
	// localPort := string(buf[:n])

	// Assign port and subdomain
	portMutex.Lock()
	serverPort := nextPort
	for activePorts[serverPort] {
		serverPort++
	}
	nextPort = serverPort + 1
	activePorts[serverPort] = true
	portMutex.Unlock()

	subdomain := fmt.Sprintf("%d.tunnel.yourdomain.com", serverPort)
	hostMap[subdomain] = serverPort

	// Configure Caddy
	err = configureCaddy(subdomain, serverPort)
	if err != nil {
		log.Println("Caddy config error:", err)
		return
	}

	// Start port listener
	go startPortListener(serverPort, session)

	// Keep connection open
	<-context.Background().Done()
}

func startPortListener(port int, session *yamux.Session) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		log.Println("Port listen error:", err)
		return
	}
	defer ln.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println("Accept error:", err)
			continue
		}

		go func(c net.Conn) {
			defer c.Close()
			stream, err := session.Open()
			if err != nil {
				return
			}
			defer stream.Close()

			go io.Copy(c, stream)
			io.Copy(stream, c)
		}(conn)
	}
}

func configureCaddy(subdomain string, port int) error {
	config := map[string]interface{}{
		"@id": subdomain,
		"match": []map[string]interface{}{{
			"host": []string{subdomain},
		}},
		"handle": []map[string]interface{}{{
			"handler": "reverse_proxy",
			"upstreams": []map[string]interface{}{{
				"dial": fmt.Sprintf("localhost:%d", port),
			}},
		}},
	}

	jsonData, _ := json.Marshal(config)
	req, _ := http.NewRequest(
		"POST",
		"http://localhost:2019/config/apps/http/servers/srv0/routes",
		bytes.NewBuffer(jsonData),
	)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

func handleHTTPRequest(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	port, exists := hostMap[host]
	if !exists {
		http.Error(w, "Tunnel not found", http.StatusNotFound)
		return
	}

	// Forward request to local port
	http.Redirect(w, r, fmt.Sprintf("http://localhost:%d%s", port, r.URL.Path), http.StatusFound)
}
