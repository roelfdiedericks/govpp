// Copyright (c) 2024 TCP connection example for GoVPP.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Example demonstrating TCP connection to VPP API.
package main

import (
	"flag"
	"fmt"
	"log"

	"go.fd.io/govpp"
	"go.fd.io/govpp/api"
	interfaces "go.fd.io/govpp/binapi/interface"
	"go.fd.io/govpp/binapi/interface_types"
	"go.fd.io/govpp/binapi/vpe"
)

var (
	tcpAddr   = flag.String("tcp", "localhost:5002", "TCP address of VPP API")
	unixSock  = flag.String("unix", "", "Unix socket path (overrides TCP if set)")
	useScheme = flag.Bool("scheme", false, "Use connection string with scheme prefix")
)

func main() {
	flag.Parse()

	// Determine connection string
	var connStr string
	if *unixSock != "" {
		if *useScheme {
			connStr = "unix://" + *unixSock
		} else {
			connStr = *unixSock
		}
		log.Printf("Connecting to VPP via Unix socket: %s", connStr)
	} else {
		if *useScheme {
			connStr = "tcp://" + *tcpAddr
		} else {
			connStr = *tcpAddr
		}
		log.Printf("Connecting to VPP via TCP: %s", connStr)
	}

	// Connect to VPP
	conn, err := govpp.Connect(connStr)
	if err != nil {
		log.Fatalf("Failed to connect to VPP: %v", err)
	}
	defer conn.Disconnect()

	log.Println("Connected to VPP")

	// Create API channel
	ch, err := conn.NewAPIChannel()
	if err != nil {
		log.Fatalf("Failed to create API channel: %v", err)
	}
	defer ch.Close()

	// Get VPP version
	showVersion(ch)

	// List interfaces
	listInterfaces(ch)
}

func showVersion(ch api.Channel) {
	req := &vpe.ShowVersion{}
	reply := &vpe.ShowVersionReply{}

	if err := ch.SendRequest(req).ReceiveReply(reply); err != nil {
		log.Printf("Failed to get VPP version: %v", err)
		return
	}

	fmt.Printf("\nVPP Version Information:\n")
	fmt.Printf("  Program:        %s\n", reply.Program)
	fmt.Printf("  Version:        %s\n", reply.Version)
	fmt.Printf("  Build Date:     %s\n", reply.BuildDate)
	fmt.Printf("  Build Directory:%s\n", reply.BuildDirectory)
	fmt.Println()
}

func listInterfaces(ch api.Channel) {
	fmt.Println("VPP Interfaces:")
	fmt.Println("===============")

	// Dump all interfaces
	reqCtx := ch.SendMultiRequest(&interfaces.SwInterfaceDump{
		SwIfIndex: interface_types.InterfaceIndex(^uint32(0)), // All interfaces
	})

	for {
		msg := &interfaces.SwInterfaceDetails{}
		stop, err := reqCtx.ReceiveReply(msg)
		if stop {
			break
		}
		if err != nil {
			log.Printf("Failed to dump interfaces: %v", err)
			return
		}

		// Convert admin and link status
		adminState := "down"
		if msg.Flags&interface_types.IF_STATUS_API_FLAG_ADMIN_UP != 0 {
			adminState = "up"
		}
		linkState := "down"
		if msg.Flags&interface_types.IF_STATUS_API_FLAG_LINK_UP != 0 {
			linkState = "up"
		}

		fmt.Printf("  [%d] %s\n", msg.SwIfIndex, msg.InterfaceName)
		fmt.Printf("      Admin: %s, Link: %s\n", adminState, linkState)
		// Check if MAC address is not all zeros
		if msg.L2Address[0] != 0 || msg.L2Address[1] != 0 || msg.L2Address[2] != 0 ||
			msg.L2Address[3] != 0 || msg.L2Address[4] != 0 || msg.L2Address[5] != 0 {
			fmt.Printf("      MAC: %02x:%02x:%02x:%02x:%02x:%02x\n",
				msg.L2Address[0], msg.L2Address[1], msg.L2Address[2],
				msg.L2Address[3], msg.L2Address[4], msg.L2Address[5])
		}
		fmt.Printf("      MTU: L3=%d, IP4=%d, IP6=%d\n",
			msg.Mtu[0], msg.Mtu[1], msg.Mtu[2])
		fmt.Println()
	}
}
