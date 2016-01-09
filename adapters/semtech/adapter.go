// Copyright © 2015 The Things Network
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package semtech

import (
	"fmt"
	"github.com/thethingsnetwork/core"
	components "github.com/thethingsnetwork/core/refactored_components"
	"github.com/thethingsnetwork/core/semtech"
	"github.com/thethingsnetwork/core/utils/log"
	"net"
)

type Adapter struct {
	loggers []log.Logger // 0 to several loggers to get feedback from the Adapter.
	conn    chan udpMsg
	next    chan rxpkMsg
}

type udpMsg struct {
	conn *net.UDPConn // Provide if you intent to change the current adapter connection
	addr *net.UDPAddr // The target recipient address
	raw  []byte       // The raw byte sequence that has to be sent
}

type rxpkMsg struct {
	rxpk      semtech.RXPK
	recipient core.Recipient
}

var ErrInvalidPort error = fmt.Errorf("Invalid port supplied. The connection might be already taken")
var ErrNotInitialized error = fmt.Errorf("Illegal call on non-initialized adapter")

// New constructs and allocates a new udp_sender adapter
func NewAdapter(port uint, loggers ...log.Logger) (*Adapter, error) {
	a := Adapter{
		loggers: loggers,
		conn:    make(chan udpMsg),
		next:    make(chan rxpkMsg),
	}

	// Create the udp connection and start listening with a goroutine
	var udpConn *net.UDPConn
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("0.0.0.0:%d", port))
	if udpConn, err = net.ListenUDP("udp", addr); err != nil {
		a.log("Unable to establish the connection: %v", err)
		return nil, ErrInvalidPort
	}

	go a.monitorConnection()
	a.conn <- udpMsg{conn: udpConn}
	go a.listen(udpConn) // Terminates when the connection is closed

	return &a, nil
}

// ok controls whether or not the adapter has been initialized via NewAdapter()
func (a *Adapter) ok() bool {
	return a != nil && a.conn != nil && a.next != nil
}

// Send implements the core.Adapter interface
func (a *Adapter) Send(p core.Packet, r ...core.Recipient) error {
	return fmt.Errorf("Unsupported method")
}

// Next implements the core.Adapter interface
func (a *Adapter) Next() (core.Packet, core.AckNacker, error) {
	if !a.ok() {
		return core.Packet{}, nil, ErrNotInitialized
	}
	msg := <-a.next
	packet, err := components.ConvertRXPK(msg.rxpk)
	if err != nil {
		return core.Packet{}, nil, err
	}
	return packet, semtechAckNacker{recipient: msg.recipient, conn: a.conn}, nil
}

// listen Handle incoming packets and forward them
func (a *Adapter) listen(conn *net.UDPConn) {
	defer conn.Close()
	a.log("Start listening on %s", conn.LocalAddr())
	for {
		buf := make([]byte, 128)
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil { // Problem with the connection
			a.log("Error: %v", err)
			continue
		}
		a.log("Incoming datagram %x", buf[:n])

		pkt, err := semtech.Unmarshal(buf[:n])
		if err != nil {
			a.log("Error: %v", err)
			continue
		}

		switch pkt.Identifier {
		case semtech.PULL_DATA: // PULL_DATA -> Respond to the recipient with an ACK
			pullAck, err := semtech.Marshal(semtech.Packet{
				Version:    semtech.VERSION,
				Token:      pkt.Token,
				Identifier: semtech.PUSH_ACK,
			})
			if err != nil {
				a.log("Unexpected error while marshaling PULL_ACK: %v", err)
				continue
			}
			a.conn <- udpMsg{addr: addr, raw: pullAck}
		case semtech.PUSH_DATA: // PUSH_DATA -> Transfer all RXPK to the component
			if pkt.Payload == nil {
				a.log("Inconsistent PUSH_DATA packet %v", pkt)
				continue
			}

			for _, rxpk := range pkt.Payload.RXPK {
				a.next <- rxpkMsg{
					rxpk:      rxpk,
					recipient: core.Recipient{Address: addr, Id: pkt.GatewayId},
				}
			}
		default:
			a.log("Unexpected packet received. Ignored: %v", pkt)
			continue
		}
	}
}

// monitorConnection manages udpConnection of the adapter and send message through that connection
func (a *Adapter) monitorConnection() {
	var udpConn *net.UDPConn
	for msg := range a.conn {
		if msg.conn != nil { // Change the connection
			if udpConn != nil {
				a.log("Define new UDP connection")
				udpConn.Close()
			}
			udpConn = msg.conn
		}

		if udpConn != nil && msg.raw != nil { // Send the given udp message
			if _, err := udpConn.WriteToUDP(msg.raw, msg.addr); err != nil {
				a.log("Unable to send udp message: %+v", err)
			}
		}
	}
	if udpConn != nil {
		udpConn.Close() // Make sure we close the connection before leaving if we dare ever leave.
	}
}

func (a *Adapter) log(format string, i ...interface{}) {
	for _, logger := range a.loggers {
		logger.Log(format, i...)
	}
}
