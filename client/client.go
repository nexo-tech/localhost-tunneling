package main

import (
	"io"
	"log"
	"net"
	"os"

	"github.com/hashicorp/yamux"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: client <local-port>")
	}
	localPort := os.Args[1]

	conn, err := net.Dial("tcp", "<VM-IP>:4000")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// Send local port to server
	_, err = conn.Write([]byte(localPort))
	if err != nil {
		log.Fatal(err)
	}

	// Yamux client session
	session, err := yamux.Client(conn, nil)
	if err != nil {
		log.Fatal(err)
	}

	for {
		stream, err := session.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go handleStream(stream, localPort)
	}
}

func handleStream(stream net.Conn, port string) {
	defer stream.Close()

	localConn, err := net.Dial("tcp", "localhost:"+port)
	if err != nil {
		log.Println(err)
		return
	}
	defer localConn.Close()

	go io.Copy(stream, localConn)
	io.Copy(localConn, stream)
}
